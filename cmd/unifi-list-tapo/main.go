package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/unpoller/unifi"
)

var (
	flagUsername = flag.String("u", "", "Unifi controller username")
	flagPassword = flag.String("p", "", "Unifi controller password")
	flagURL      = flag.String("U", "http://127.0.0.1:8443", "Unifi controller URL")
	flagSiteName = flag.String("s", "default", "Site name")
	flagOutfile  = flag.String("o", "tapo_devices.txt", "Output file")
)

func isTapoPlugWithMeterByName(client *unifi.Client) bool {
	for _, k := range []string{
		strings.ToLower(client.Name),
		strings.ToLower(client.Hostname),
	} {
		if k == "p110" ||
			k == "p115" ||
			strings.Contains(k, "tapo") ||
			strings.Contains(k, "Tapo") {
			return true
		}
	}
	return false
}

func main() {
	flag.Parse()
	c := unifi.Config{
		User:     *flagUsername,
		Pass:     *flagPassword,
		URL:      *flagURL,
		ErrorLog: log.Printf,
		DebugLog: nil,
	}
	uni, err := unifi.NewUnifi(&c)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	sites, err := uni.GetSites()
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	siteIdx := -1
	for idx, site := range sites {
		if site.Name == *flagSiteName {
			siteIdx = idx
		}
	}
	if siteIdx == -1 {
		log.Fatalf("Site '%s' not found", *flagSiteName)
	}
	clients, err := uni.GetClients([]*unifi.Site{sites[siteIdx]})
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	var output string
	idx := 0
	for _, client := range clients {
		if isTapoPlugWithMeterByName(client) {
			// NOTE: these device names only get Tapo P100 and P110 plugs
			fmt.Println(idx+1, client.ID, client.Hostname, client.IP, client.Name, client.Mac, client.LastSeen)
			output += fmt.Sprintf("%s\n", client.IP)
			idx++
		}
	}
	// NOTE: output is unsorted
	if err := os.WriteFile(*flagOutfile, []byte(output), 0644); err != nil {
		log.Fatalf("Failed to write to file '%s': %v", *flagOutfile, err)
	}
	fmt.Printf("Written to file '%s'\n", *flagOutfile)
}
