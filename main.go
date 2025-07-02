package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/insomniacslk/tapo"
	"github.com/insomniacslk/xjson"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	flagPath            = flag.String("p", "/metrics", "HTTP path where to expose metrics to")
	flagListen          = flag.String("l", ":9105", "Address to listen to")
	flagConfigFile      = flag.String("c", "config.json", "Configuration file")
	flagSleepInterval   = flag.Duration("i", time.Minute, "Interval between reading updates, expressed as a Go duration string")
	flagRetryInterval   = flag.Duration("R", 2*time.Second, "Interval between attempts to read a device's info, expressed as a Go duration string")
	flagStopOnKlapError = flag.Bool("k", false, "Stop the exporter if login fails on a plug because of unsupported KLAP protocol")
)

// Config is the configuration file type.
type Config struct {
	Username   string       `json:"username"`
	Password   string       `json:"password"`
	Devices    []netip.Addr `json:"devices"`
	DevicesURL *xjson.URL   `json:"devices_url,omitempty"`
}

var modelsWithPowerInformation = []string{
	// NOTE: these are the ones known to me. Feel free to suggest to add more
	"P110",
	"P115",
	"P125M",
}

// LoadConfig loads the configuration file into a Config type.
func LoadConfig(filepath string) (*Config, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON config: %w", err)
	}
	return &config, nil
}

func makeGauge(name, help string, attributes []string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: name,
			Help: help,
		},
		attributes,
	)
}

// gauges for Tapo's smart plugs.
var (
	deviceInfoAttributes = []string{
		"device_id", "nickname", "model", "mac", "oem_id",
	}
	deviceInfoAllAttributes = []string{
		"device_id", "nickname", "model", "mac", "oem_id",
		"fw_version", "hw_version", "type", "hw_id", "fw_id", "ip", "time_diff", "ssid", "rssi", "signal_level",
		"latitude", "longitude", "lang", "avatar", "region", "specs", "has_set_location_info", "device_on", "on_time",
		"overheated", "power_protection_status", "location",
	}
	deviceRequestFailedAttributes = []string{"ip_address", "error"}

	deviceInfoGauge          = makeGauge("tapo_device_info", "Tapo plug - Device info", deviceInfoAllAttributes)
	deviceRequestFailedGauge = makeGauge("tapo_device_request_failed", "Tapo plug - Device request failed", deviceRequestFailedAttributes)

	deviceOnGauge         = makeGauge("tapo_plug_device_on", "Tapo plug - device on", deviceInfoAttributes)
	deviceOverheatedGauge = makeGauge("tapo_plug_device_overheated", "Tapo plug - device overheated", deviceInfoAttributes)

	timeUsageTodayGauge  = makeGauge("tapo_plug_time_usage_today", "Tapo plug - time usage today", deviceInfoAttributes)
	timeUsagePast7Gauge  = makeGauge("tapo_plug_time_usage_past7", "Tapo plug - time usage past 7 days", deviceInfoAttributes)
	timeUsagePast30Gauge = makeGauge("tapo_plug_time_usage_past30", "Tapo plug - time usage past 30 days", deviceInfoAttributes)

	powerUsageTodayGauge  = makeGauge("tapo_plug_power_usage_today", "Tapo plug - power usage today", deviceInfoAttributes)
	powerUsagePast7Gauge  = makeGauge("tapo_plug_power_usage_past7", "Tapo plug - power usage past 7 days", deviceInfoAttributes)
	powerUsagePast30Gauge = makeGauge("tapo_plug_power_usage_past30", "Tapo plug - power usage past 30 days", deviceInfoAttributes)

	savedPowerTodayGauge  = makeGauge("tapo_plug_saved_power_today", "Tapo plug - saved power today", deviceInfoAttributes)
	savedPowerPast7Gauge  = makeGauge("tapo_plug_saved_usage_past7", "Tapo plug - saved power past 7 days", deviceInfoAttributes)
	savedPowerPast30Gauge = makeGauge("tapo_plug_saved_power_past30", "Tapo plug - saved power past 30 days", deviceInfoAttributes)

	todayRuntimeGauge = makeGauge("tapo_plug_today_runtime", "Tapo plug - today runtime", deviceInfoAttributes)
	monthRuntimeGauge = makeGauge("tapo_plug_month_runtime", "Tapo plug - month runtime", deviceInfoAttributes)
	todayEnergyGauge  = makeGauge("tapo_plug_today_energy", "Tapo plug - today energy", deviceInfoAttributes)
	monthEnergyGauge  = makeGauge("tapo_plug_month_energy", "Tapo plug - month energy", deviceInfoAttributes)

	electricityCharge0Gauge = makeGauge("tapo_plug_electricity_charge_0", "Tapo plug - electricity charge 0", deviceInfoAttributes)
	electricityCharge1Gauge = makeGauge("tapo_plug_electricity_charge_1", "Tapo plug - electricity charge 1", deviceInfoAttributes)
	electricityCharge2Gauge = makeGauge("tapo_plug_electricity_charge_2", "Tapo plug - electricity charge 2", deviceInfoAttributes)

	currentPowerGauge = makeGauge("tapo_plug_current_power", "Tapo plug - current power", deviceInfoAttributes)
)

func validateDevices(devices []netip.Addr) ([]netip.Addr, error) {
	if len(devices) == 0 {
		return nil, fmt.Errorf("device list is empty")
	}
	// sanity checks, and remove duplicates
	tmap := make(map[netip.Addr]struct{})
	for _, d := range devices {
		if _, exists := tmap[d]; exists {
			log.Printf("Ignoring duplicate device %s", d)
		}
		tmap[d] = struct{}{}
	}
	uniqueDevices := make([]netip.Addr, 0, len(tmap))
	for d := range tmap {
		uniqueDevices = append(uniqueDevices, d)
	}
	if len(uniqueDevices) == 0 {
		return nil, fmt.Errorf("got zero valid devices")
	}
	return uniqueDevices, nil
}

func main() {
	flag.Parse()
	config, err := LoadConfig(*flagConfigFile)
	if err != nil {
		log.Fatalf("Failed to load configuration file '%s': %v", *flagConfigFile, err)
	}
	devices := config.Devices
	if config.DevicesURL != nil {
		// also get a device list from an URL
		var (
			data []byte
			err  error
		)
		log.Printf("Retrieving devices list from '%s'", *config.DevicesURL)
		if config.DevicesURL.Scheme == "file" {
			filePath := path.Join(config.DevicesURL.Host, config.DevicesURL.Path)
			data, err = os.ReadFile(filePath)
			if err != nil {
				log.Fatalf("Failed to read '%s': %v", filePath, err)
			}
		} else {
			var resp *http.Response
			resp, err = http.Get((*config.DevicesURL).String())
			if err != nil {
				log.Fatalf("Failed to retrieve devices URL '%s': %v", *config.DevicesURL, err)
			}
			if resp.StatusCode != 200 {
				_ = resp.Body.Close()
				log.Fatalf("HTTP request failed, expected 200 OK, got %s", resp.Status)
			}
			data, err = io.ReadAll(resp.Body)
			if err != nil {
				_ = resp.Body.Close()
				log.Fatalf("Failed to read HTTP body: %v", err)
			}
			_ = resp.Body.Close()
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		count := 0
		for scanner.Scan() {
			line := scanner.Text()
			addr, err := netip.ParseAddr(line)
			if err != nil {
				log.Printf("Skip invalid IP address '%s'", line)
				continue
			}
			devices = append(devices, addr)
			count++
		}
		log.Printf("Got %d devices from URL", count)
	}
	devices, err = validateDevices(devices)
	if err != nil {
		log.Fatalf("Device validation failed: %v", err)
	}
	allPlugs := make([]*tapo.Plug, 0, len(devices))
	for _, addr := range devices {
		allPlugs = append(allPlugs, tapo.NewPlug(addr, nil))
	}
	fmt.Printf("Trying to log in to %d Tapo plugs\n", len(allPlugs))
	plugLogin := func(plug *tapo.Plug, username, password string, stopOnKlapError bool) error {
		if err := plug.Handshake(username, password); err != nil {
			log.Printf("Error: login failed for plug %s: %v", plug.Addr, err)
			// some devices with recent firmware require the newer KLAP
			// protocol from TP-Link, and will fail login until it is
			// implemented. Handle this error specifically.
			var te tapo.TapoError
			if !stopOnKlapError && errors.As(err, &te) {
				if te == 1003 {
					log.Printf("Warning: login failed for plug %s, continuing because it's probably a firmware with the new KLAP protocol': %v", plug.Addr, err)
					return nil
				}
			}
			return err
		}
		return nil
	}
	plugs := make([]*tapo.Plug, 0)
	for _, plug := range allPlugs {
		if err := plugLogin(plug, config.Username, config.Password, *flagStopOnKlapError); err != nil {
			log.Printf("Error: login failed for plug '%s': %v", plug.Addr, err)
		}
		plugs = append(plugs, plug)
	}
	fmt.Printf("Monitoring %d Tapo plugs (ignored %d plugs)\n", len(plugs), len(allPlugs)-len(plugs))

	// register gauges
	if err := prometheus.Register(deviceInfoGauge); err != nil {
		log.Fatalf("Failed to register device_info gauge: %v", err)
	}
	if err := prometheus.Register(deviceRequestFailedGauge); err != nil {
		log.Fatalf("Failed to register device_request_failed gauge: %v", err)
	}
	if err := prometheus.Register(deviceOnGauge); err != nil {
		log.Fatalf("Failed to register device_on gauge: %v", err)
	}
	if err := prometheus.Register(deviceOverheatedGauge); err != nil {
		log.Fatalf("Failed to register device_overheated gauge: %v", err)
	}
	if err := prometheus.Register(timeUsageTodayGauge); err != nil {
		log.Fatalf("Failed to register time_usage_today gauge: %v", err)
	}
	if err := prometheus.Register(timeUsagePast7Gauge); err != nil {
		log.Fatalf("Failed to register time_usage_past7 gauge: %v", err)
	}
	if err := prometheus.Register(timeUsagePast30Gauge); err != nil {
		log.Fatalf("Failed to register time_usage_past30 gauge: %v", err)
	}
	if err := prometheus.Register(powerUsageTodayGauge); err != nil {
		log.Fatalf("Failed to register power_usage_today gauge: %v", err)
	}
	if err := prometheus.Register(powerUsagePast7Gauge); err != nil {
		log.Fatalf("Failed to register power_usage_past7 gauge: %v", err)
	}
	if err := prometheus.Register(powerUsagePast30Gauge); err != nil {
		log.Fatalf("Failed to register power_usage_past30 gauge: %v", err)
	}
	if err := prometheus.Register(savedPowerTodayGauge); err != nil {
		log.Fatalf("Failed to register saved_power_today gauge: %v", err)
	}
	if err := prometheus.Register(savedPowerPast7Gauge); err != nil {
		log.Fatalf("Failed to register saved_power_past7 gauge: %v", err)
	}
	if err := prometheus.Register(savedPowerPast30Gauge); err != nil {
		log.Fatalf("Failed to register saved_power_past30 gauge: %v", err)
	}
	if err := prometheus.Register(todayRuntimeGauge); err != nil {
		log.Fatalf("Failed to register today_runtime_gauge gauge: %v", err)
	}
	if err := prometheus.Register(monthRuntimeGauge); err != nil {
		log.Fatalf("Failed to register month_runtime_gauge gauge: %v", err)
	}
	if err := prometheus.Register(todayEnergyGauge); err != nil {
		log.Fatalf("Failed to register today_energy_gauge gauge: %v", err)
	}
	if err := prometheus.Register(monthEnergyGauge); err != nil {
		log.Fatalf("Failed to register month_energy_gauge gauge: %v", err)
	}
	if err := prometheus.Register(electricityCharge0Gauge); err != nil {
		log.Fatalf("Failed to register electricity_charge_0_gauge gauge: %v", err)
	}
	if err := prometheus.Register(electricityCharge1Gauge); err != nil {
		log.Fatalf("Failed to register electricity_charge_1_gauge gauge: %v", err)
	}
	if err := prometheus.Register(electricityCharge2Gauge); err != nil {
		log.Fatalf("Failed to register electricity_charge_2_gauge gauge: %v", err)
	}
	if err := prometheus.Register(currentPowerGauge); err != nil {
		log.Fatalf("Failed to register current_power gauge: %v", err)
	}

	go func() {
		for {
			for _, plug := range plugs {
				log.Printf("Fetching metrics for plug %s", plug.Addr)
				plug = tapo.NewPlug(plug.Addr, nil)
				if err := plugLogin(plug, config.Username, config.Password, *flagStopOnKlapError); err != nil {
					deviceRequestFailedGauge.WithLabelValues(plug.Addr.String(), err.Error()).Inc()
					log.Printf("Warning: failed to log in on plug '%s': %v", plug.Addr, err)
					continue
				}
				// TODO parallelize
				var i *tapo.DeviceInfo
				const maxAttempts = 3
				for attempt := 1; attempt <= maxAttempts; attempt++ {
					i, err = plug.GetDeviceInfo()
					if err != nil {
						deviceRequestFailedGauge.WithLabelValues(plug.Addr.String(), err.Error()).Inc()
						log.Printf("GetDeviceInfo for plug '%s' failed at attempt %d, trying again in %s: %v", plug.Addr, attempt, *flagRetryInterval, err)
						if attempt < maxAttempts {
							time.Sleep(*flagRetryInterval)
						}
					} else {
						break
					}
				}
				if err != nil {
					log.Fatalf("GetDeviceInfo failed after 3 attempts. Last error: %v", err)
				}
				var u *tapo.DeviceUsage
				for attempt := 1; attempt <= maxAttempts; attempt++ {
					u, err = plug.GetDeviceUsage()
					if err != nil {
						deviceRequestFailedGauge.WithLabelValues(plug.Addr.String(), err.Error()).Inc()
						log.Printf("GetDeviceUsage for plug '%s' failed at attempt %d, trying again in %s: %v", plug.Addr, attempt, *flagRetryInterval, err)
						if attempt < maxAttempts {
							time.Sleep(*flagRetryInterval)
						}
					} else {
						break
					}
				}
				if err != nil {
					log.Fatalf("GetDeviceUsage failed after 3 attempts. Last error: %v", err)
				}
				var e *tapo.EnergyUsage
				// TODO always try to get energy usage without relying on a
				// hardcoded list
				hasPowerInformation := false
				for _, m := range modelsWithPowerInformation {
					if strings.ToLower(i.Model) == strings.ToLower(m) {
						hasPowerInformation = true
					}
				}
				if hasPowerInformation {
					for attempt := 1; attempt <= maxAttempts; attempt++ {
						e, err = plug.GetEnergyUsage()
						if err != nil {
							deviceRequestFailedGauge.WithLabelValues(plug.Addr.String(), err.Error()).Inc()
							log.Printf("GetEnergyUsage for plug '%s' failed at attempt %d, trying again in %s: %v", plug.Addr, attempt, *flagRetryInterval, err)
							if attempt < maxAttempts {
								time.Sleep(*flagRetryInterval)
							}
						} else {
							break
						}
					}
					if err != nil {
						log.Fatalf("GetEnergyUsage failed after 3 attempts. Last error: %v", err)
					}
				} else {
					log.Printf("Ignoring device without power information ip=%s, model=%s", i.IP, i.Model)
				}
				labels := []string{
					i.DeviceID, i.DecodedNickname, i.Model, i.MAC, i.OEMID,
				}
				allLabels := append(
					labels,
					i.FWVersion,
					i.HWVersion,
					i.Type,
					i.HWID,
					i.FWID,
					i.IP,
					strconv.FormatInt(int64(i.TimeDiff), 10),
					i.DecodedSSID,
					strconv.FormatInt(int64(i.RSSI), 10),
					strconv.FormatInt(int64(i.SignalLevel), 10),
					strconv.FormatInt(int64(i.Latitude), 10),
					strconv.FormatInt(int64(i.Longitude), 10),
					i.Lang,
					i.Avatar,
					i.Region,
					i.Specs,
					strconv.FormatBool(i.HasSetLocationInfo),
					strconv.FormatBool(i.DeviceON),
					strconv.FormatInt(int64(i.OnTime), 10),
					strconv.FormatBool(i.OverHeated),
					i.PowerProtectionStatus,
					i.Location,
				)
				deviceInfoGauge.WithLabelValues(allLabels...).Set(1)
				if i.DeviceON {
					deviceOnGauge.WithLabelValues(labels...).Set(1)
				} else {
					deviceOnGauge.WithLabelValues(labels...).Set(0)
				}
				if i.OverHeated {
					deviceOverheatedGauge.WithLabelValues(labels...).Set(1)
				} else {
					deviceOverheatedGauge.WithLabelValues(labels...).Set(0)
				}
				timeUsageTodayGauge.WithLabelValues(labels...).Set(float64(u.TimeUsage.Today))
				timeUsagePast7Gauge.WithLabelValues(labels...).Set(float64(u.TimeUsage.Past7))
				timeUsagePast30Gauge.WithLabelValues(labels...).Set(float64(u.TimeUsage.Past30))
				powerUsageTodayGauge.WithLabelValues(labels...).Set(float64(u.PowerUsage.Today))
				powerUsagePast7Gauge.WithLabelValues(labels...).Set(float64(u.PowerUsage.Past7))
				powerUsagePast30Gauge.WithLabelValues(labels...).Set(float64(u.PowerUsage.Past30))
				savedPowerTodayGauge.WithLabelValues(labels...).Set(float64(u.SavedPower.Today))
				savedPowerPast7Gauge.WithLabelValues(labels...).Set(float64(u.SavedPower.Past7))
				savedPowerPast30Gauge.WithLabelValues(labels...).Set(float64(u.SavedPower.Past30))
				if e != nil {
					todayRuntimeGauge.WithLabelValues(labels...).Set(float64(e.TodayRuntime))
					monthRuntimeGauge.WithLabelValues(labels...).Set(float64(e.MonthRuntime))
					todayEnergyGauge.WithLabelValues(labels...).Set(float64(e.TodayEnergy))
					monthEnergyGauge.WithLabelValues(labels...).Set(float64(e.MonthEnergy))
					electricityCharge0Gauge.WithLabelValues(labels...).Set(float64(e.ElectricityCharge[0]))
					electricityCharge1Gauge.WithLabelValues(labels...).Set(float64(e.ElectricityCharge[1]))
					electricityCharge2Gauge.WithLabelValues(labels...).Set(float64(e.ElectricityCharge[2]))
					currentPowerGauge.WithLabelValues(labels...).Set(float64(e.CurrentPower))
				}
			}
			log.Printf("Sleeping %s...", *flagSleepInterval)
			time.Sleep(*flagSleepInterval)
		}
	}()

	http.Handle(*flagPath, promhttp.Handler())
	log.Printf("Starting server on %s", *flagListen)
	log.Fatal(http.ListenAndServe(*flagListen, nil))
}
