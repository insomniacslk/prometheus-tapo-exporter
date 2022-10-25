package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/netip"
	"time"

	"github.com/insomniacslk/tapo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	flagPath          = flag.String("p", "/metrics", "HTTP path where to expose metrics to")
	flagListen        = flag.String("l", ":9105", "Address to listen to")
	flagConfigFile    = flag.String("c", "config.json", "Configuration file")
	flagSleepInterval = flag.Duration("i", time.Minute, "Interval between speedtest executions, expressed as a Go duration string")
)

// Config is the configuration file type.
type Config struct {
	Username string       `json:"username"`
	Password string       `json:"password"`
	Devices  []netip.Addr `json:"devices"`
}

// LoadConfig loads the configuration file into a Config type.
func LoadConfig(filepath string) (*Config, error) {
	data, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON config: %w", err)
	}
	return &config, nil
}

func makeGauge(name, help string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: name,
			Help: help,
		},
		deviceInfoAttributes,
	)
}

// gauges for Tapo's smart plugs.
var (
	deviceInfoAttributes = []string{
		"device_id", "nickname", "model", "mac", "oem_id",
	}

	deviceOnGauge = makeGauge("tapo_plug_device_on", "Tapo plug - device on")

	timeUsageTodayGauge  = makeGauge("tapo_plug_time_usage_today", "Tapo plug - time usage today")
	timeUsagePast7Gauge  = makeGauge("tapo_plug_time_usage_past7", "Tapo plug - time usage past 7 days")
	timeUsagePast30Gauge = makeGauge("tapo_plug_time_usage_past30", "Tapo plug - time usage past 30 days")

	powerUsageTodayGauge  = makeGauge("tapo_plug_power_usage_today", "Tapo plug - power usage today")
	powerUsagePast7Gauge  = makeGauge("tapo_plug_power_usage_past7", "Tapo plug - power usage past 7 days")
	powerUsagePast30Gauge = makeGauge("tapo_plug_power_usage_past30", "Tapo plug - power usage past 30 days")

	savedPowerTodayGauge  = makeGauge("tapo_plug_saved_power_today", "Tapo plug - saved power today")
	savedPowerPast7Gauge  = makeGauge("tapo_plug_saved_usage_past7", "Tapo plug - saved power past 7 days")
	savedPowerPast30Gauge = makeGauge("tapo_plug_saved_power_past30", "Tapo plug - saved power past 30 days")

	todayRuntimeGauge = makeGauge("tapo_plug_today_runtime", "Tapo plug - today runtime")
	monthRuntimeGauge = makeGauge("tapo_plug_month_runtime", "Tapo plug - month runtime")
	todayEnergyGauge  = makeGauge("tapo_plug_today_energy", "Tapo plug - today energy")
	monthEnergyGauge  = makeGauge("tapo_plug_month_energy", "Tapo plug - month energy")

	electricityCharge0Gauge = makeGauge("tapo_plug_electricity_charge_0", "Tapo plug - electricity charge 0")
	electricityCharge1Gauge = makeGauge("tapo_plug_electricity_charge_1", "Tapo plug - electricity charge 1")
	electricityCharge2Gauge = makeGauge("tapo_plug_electricity_charge_2", "Tapo plug - electricity charge 2")

	currentPowerGauge = makeGauge("tapo_plug_current_power", "Tapo plug - current power")
)

func validateDevices(devices []netip.Addr) ([]netip.Addr, error) {
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
	devices, err := validateDevices(config.Devices)
	if err != nil {
		log.Fatalf("Device validation failed: %v", err)
	}
	plugs := make([]*tapo.Plug, 0, len(devices))
	for _, addr := range devices {
		plugs = append(plugs, tapo.NewPlug(addr, config.Username, config.Password, nil))
	}
	fmt.Printf("Monitoring %d Tapo plugs\n", len(plugs))
	for _, plug := range plugs {
		if err := plug.Login(config.Username, config.Password); err != nil {
			log.Printf("Error: login failed for plug %s: %v", plug.Addr, err)
			return
		}
	}

	// register gauges
	if err := prometheus.Register(deviceOnGauge); err != nil {
		log.Fatalf("Failed to register device_on gauge: %v", err)
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
				// TODO parallelize
				i, err := plug.GetDeviceInfo()
				if err != nil {
					log.Printf("GetDeviceInfo failed for plug %s: %v", plug.Addr, err)
				}
				u, err := plug.GetDeviceUsage()
				if err != nil {
					log.Printf("GetDeviceUsage failed for plug %s: %v", plug.Addr, err)
				}
				var e *tapo.EnergyUsage
				if i.Model == "P110" {
					e, err = plug.GetEnergyUsage()
					if err != nil {
						log.Printf("GetEnergyUsage failed for plug %s: %v", plug.Addr, err)
					}
				}
				labels := []string{
					i.DeviceID, i.DecodedNickname, i.Model, i.MAC, i.OEMID,
				}

				if i.DeviceON {
					deviceOnGauge.WithLabelValues(labels...).Set(1)
				} else {
					deviceOnGauge.WithLabelValues(labels...).Set(0)
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
