# prometheus-tapo-exporter

This is a Prometheus exporter for TP-Link's Tapo P100/P110 smart plugs with energy measurements.

It will export the following metrics:
* `tapo_plug_device_info`
* `tapo_plug_device_on`
* `tapo_plug_device_overheated`
* `tapo_plug_time_usage_today`
* `tapo_plug_time_usage_past7`
* `tapo_plug_time_usage_past30`
* `tapo_plug_power_usage_today`
* `tapo_plug_power_usage_past7`
* `tapo_plug_power_usage_past30`
* `tapo_plug_saved_power_today`
* `tapo_plug_saved_usage_past7`
* `tapo_plug_saved_power_past30`
* `tapo_plug_today_runtime`
* `tapo_plug_month_runtime`
* `tapo_plug_today_energy`
* `tapo_plug_month_energy`
* `tapo_plug_electricity_charge_0`
* `tapo_plug_electricity_charge_1`
* `tapo_plug_electricity_charge_2`
* `tapo_plug_current_power`

## Run it

Create a configuration file similar to the following:
```
{
    "username": "your@email",
    "password": "your_password",
    "devices": [
        "10.0.0.10",
        "10.0.0.11",
        "10.0.0.12"
    ]
}
```

then build and run:
```
go build
./prometheus-tapo-exporter -c ~/config.json
```
