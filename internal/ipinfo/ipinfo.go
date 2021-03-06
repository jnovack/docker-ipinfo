package ipinfo

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oschwald/geoip2-golang"
	"github.com/rs/zerolog/log"
)

// The GeoIP databases
var dbCity *geoip2.Reader
var dbASN *geoip2.Reader

// https://github.com/multiverse-os/ip/blob/1c436abe71f332ef3d2342c7a08a8ad25ae379b9/records.go

type codename struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type ipInfo struct {
	IP           string   `json:"ip"`
	City         string   `json:"city"`
	Region       string   `json:"region"`
	Country      codename `json:"country"`
	Continent    codename `json:"continent"`
	Location     location `json:"location"`
	Postal       string   `json:"postal"`
	ASN          uint     `json:"asn"`
	Organization string   `json:"organization"`
}

// Initialize the database from a working directory (should have trailing slash)
func Initialize(workDir string) {
	var err error

	dbCity, err = geoip2.Open(workDir + "GeoLite2-City.mmdb")
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to open City database, cannot continue")
	}

	dbASN, err = geoip2.Open(workDir + "GeoLite2-ASN.mmdb")
	if err != nil {
		log.Warn().Err(err).Msg("Unable to open ASN database, lookups will not have ASN or Organization info")
	}

}

// Lookup the IP Address within the request.
func Lookup(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	retval := http.StatusTeapot

	var IPAddress string
	var ipinfo ipInfo

	defer func() {
		// Get the current time, so that we can then calculate the execution time.
		dur := float64(float64(time.Since(start).Nanoseconds()) / 1000000)

		duration.WithLabelValues(strconv.Itoa(retval)).Observe(dur)
		// Log how much time it took to respond to the request, when we're done.
		log.Info().
			Float64("duration", dur).
			Str("ipaddress", ipinfo.IP).
			Str("method", r.Method).
			Str("remote", defangIP(r.RemoteAddr)).
			Str("url", r.URL.EscapedPath()).
			Int("status", retval).
			Msg("")
	}()

	// IP addresses will never be longer than 46 characters
	// IPv4 = 255.255.255.255 (slash + 15 characters)
	// IPv6 = ABCD:ABCD:ABCD:ABCD:ABCD:ABCD:ABCD:ABCD (slash + 39 characters)
	// IPv4-mapped IPv6 = ABCD:ABCD:ABCD:ABCD:ABCD:ABCD:192.168.158.190 (slash + 45 characters)
	if len(r.URL.Path) > 46 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		retval = http.StatusForbidden
		return
	}

	IPAddress = strings.Split(r.URL.Path, "/")[1]

	// Set the requested IP to the user's request request IP, if we got no address.
	if IPAddress == "" || IPAddress == "self" || IPAddress == "me" {
		// The request is most likely being done through a reverse proxy.
		if realIP, ok := r.Header["X-Real-Ip"]; ok && len(r.Header["X-Real-Ip"]) > 0 {
			IPAddress = realIP[0]
		} else {
			// Get the real actual request IP without the trolls
			IPAddress = defangIP(r.RemoteAddr)
		}
	}

	ip := net.ParseIP(IPAddress)
	if ip == nil {
		http.Error(w, "Unprocessable Entity", http.StatusUnprocessableEntity)
		retval = http.StatusUnprocessableEntity
		return
	}

	ipinfo.IP = ip.String()

	// Query the maxmind database for that IP address.
	recCity, err := dbCity.City(ip)
	if err != nil {
		log.Warn().Err(err).Str("ip", ip.String()).Msg("Warning: Unable to lookup in City database")
	}

	// Query the maxmind database for that IP address, if we have the ASN database.
	if dbASN != nil {
		recASN, err := dbASN.ASN(ip)
		if err != nil {
			log.Warn().Err(err).Str("ip", ip.String()).Msg("Warning: Unable to lookup in ASN database")
		} else {
			ipinfo.ASN = recASN.AutonomousSystemNumber
			ipinfo.Organization = recASN.AutonomousSystemOrganization
		}
	}

	// String containing the region/subdivision of the IP. (E.g.: Scotland, or California).
	// If there are subdivisions for this IP, set sd as the first element in the array's name.
	if recCity.Subdivisions != nil {
		ipinfo.Region = recCity.Subdivisions[0].Names[*Locale]
	}

	ipinfo.City = recCity.City.Names[*Locale]

	ipinfo.Country = codename{
		Code: recCity.Country.IsoCode,
		Name: recCity.Country.Names[*Locale],
	}

	ipinfo.Continent = codename{
		Code: recCity.Continent.Code,
		Name: recCity.Continent.Names[*Locale],
	}

	ipinfo.Location = location{
		Latitude:  recCity.Location.Latitude,
		Longitude: recCity.Location.Longitude,
	}

	ipinfo.Postal = recCity.Postal.Code

	// Since we don't have HTML output, nor other data from geo data,
	// everything is the same if you do /8.8.8.8, /8.8.8.8/json or /8.8.8.8/geo.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	callback := r.URL.Query().Get("callback")
	enableJSONP := callback != "" && len(callback) < 2000 && callbackJSONP.MatchString(callback)
	if enableJSONP {
		_, err = w.Write([]byte("/**/ typeof " + callback + " === 'function' " +
			"&& " + callback + "("))
		if err != nil {
			return
		}
	}
	enc := json.NewEncoder(w)
	if r.URL.Query().Get("pretty") == "1" {
		enc.SetIndent("", "  ")
	}
	enc.Encode(ipinfo)
	if enableJSONP {
		w.Write([]byte(");"))
	}

	retval = http.StatusOK
}

// Very restrictive, but this way it shouldn't completely fuck up.
var callbackJSONP = regexp.MustCompile(`^[a-zA-Z_\$][a-zA-Z0-9_\$]*$`)

// Remove from the IP eventual [ or ], and remove the port part of the IP.
func defangIP(ip string) string {
	ip = strings.Replace(ip, "[", "", 1)
	ip = strings.Replace(ip, "]", "", 1)
	ss := strings.Split(ip, ":")
	ip = strings.Join(ss[:len(ss)-1], ":")
	return ip
}
