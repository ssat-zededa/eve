# Device Connectivity

EVE needs to always be able to connect to the controller, yet the configuration of the network ports might be both complex and changing over time.

In addition some network ports might be designated to application usage, or the underlying I/O bus controllers (e.g., PCI controllers) be designated for assignment to applications. That part of the configuration can also change.

Any network port configuration changes which might affect the connectivity to the controller requires care to avoid permanently loosing connectivity to the controller and become unmanageable as a result. Thus it is required to perform testing as part of a configuration change and have a way to fall back to a working port configuration.

This is accomplished by logic to test connectivity to the controller (implemented in the Network Interface Manager [nim](../pkg/pillar/cmd/nim) with help from the [devicenetwork](../pkg/pillar/devicenetwork) package, and maintaining a list of current working and fallback configuration in ```/persist/status/nim/DevicePortConfigList/global.json```

## Sources of configuration

There are several sources from which nim gets the potential port configurations. Those all use the ```DevicePortConfig``` type. There are examples of such configurations in [legacy EVE configuration](CONFIG.md)

### Fresh install

When EVE is installed on a device it only has the last-resort configuration specified below. This is sufficient to connect to the controller if

- DHCP is enabled on one of the Ethernet ports
- WiFi is not assumed (since WiFi needs credentials)
- If cellular connectivity is assumed, the default APN will work to connect to the network
- No enterprise proxy configuration is required to be able to connect to the controller.

If any of those cases apply, then a USB stick is needed to specify the initial port configuration so that the device can connect to the controller.

### Override the configuration using a USB stick

If the deployment site requires use of HTTP enterprise proxies and/or static IP configuration, then a file containing a DevicePortConfig can be placed on a USB stick prior to booting the device. Note that this requires that the USB controller is enabled using debug.enable.usb as specified in [configuration properties](CONFIG-PROPERTIES.md)

There are examples of such configurations in [legacy EVE configuration](CONFIG.md)

Such a file will be used by nim until it can connect to the controller and receive the configuration (either the same, or subsequent updates). Thus for a new device using enterprise proxies and/or static IP it is imperative that the configuration first be set in the controller, then a USB stick be created with that configuration, and the device booted with that USB stick in place.
That ensures that the configuration doesn't revert back once the device has connected to the controller.

### From the controller

The systemAdapter in the API specifies the intended port configuration.
This is fed into the logic in nim by [zedagent](../pkg/pillar/cmd/zedagent) publishing a ```DevicePortConfig``` item.

The API for this is [SystemAdapter](../api/proto/config/devmodel.proto).
At least one port must be set to be a management port, and that port needs to refer to a network with IP configuration for the device to even try to use the SystemAdapter configuration.

### Last resort

Unless the network.fallback.any.eth configuration item is set to false (as specified in [configuration properties](CONFIG-PROPERTIES.md)), then there is an additional lowest priority item in the list of DevicePortConfigs, based on finding all of the Ethernet and Ethernet-like interfaces (an example of the latter is WiFi and cellular modems) which are not used exclusively by applications. The last resort configuration assumes DHCP and no enterprise proxies.

Note that if static IP and/or enterprise proxies are used it is useful to set network.fallback.any.eth to false to avoid having the device try DHCP without proxies when there is some network outage.

## Prioritizing the list

The nim retains the currently working configuration, plus the following in priority order in ```/persist/status/nim/DevicePortConfigList```:

1. The most recently received configuration from the controller
1. The last known working configuration from the controller
1. An override file from a USB stick (if any)
1. The last resort if so enabled

Once the most recent configuration received from the controller has been tested and found to be usable, then all but the (optional) last resort configuration are pruned from the above list.
When a new configuration is received from the controller it will keep the old configuration from the controller as a fallback.

## Testing

The Network Interface Manager performs two types of testing

- Validate that a new configuration is working before replacing the current configuration
- Periodically check that the existing configuration is working

### Testing a new configuration from the controller

The testing is triggered by receiving a new configuration from the controller and completes when at least one of the management ports can be used to reach the controller. If there are multiple management ports in the configuration, there might be an error reported for ports which are not working (depending on the order in which the ports are tried).

[TBD Should we verify that the new configuration is usable for some minimum time e.g., 10 minutes before discarding the previous/fallback configuration?]

If no management port can be used to reach the controller, then nim switches to using the next configuration in the DevicePortConfigList, which is normally the previously used configuration.
In that case a failure is reported in the [SystemAdapterInfo](../api/proto/info/info.proto) by setting lastError in DevicePortStatus and the currentIndex is set to the currently used DevicePortStatus in the list. Note that lastFailed and lastSucceeded can be used to see if a configuration has succeeded in the past or always failed.

### Periodic testing

The default timer for this is 5 minutes and can be set with [timer.port.testinterval](CONFIG-PROPERTIES.md). At those intervals the device verifies that it can still reach the controller using one of the management ports.

Each attempt it starts with a different management port, which ensures that all management ports are tested for connectivity. Any management port which sees a failure gets an error in the [SystemAdapterInfo](../api/proto/info/info.proto) in the ErrorInfo for the particular DevicePort.

Note that if a port was tested and succeeded the ErrorInfo.timestamp is updated and the ErrorInfo.description is empty; this indicates the most recent successful test.

If after two attempts (spaced the above 5 minute timer apart) the device can not connect to the controller on any of the management ports in the current configuration, then it will try the fallback configuration (and if that succeeds the fact that it is using a fallback is reported with a zero currentIndex as above).

### Trying a better configuration

If for some reason the most recent (aka highest priority) configuration is not used (and currentIndex is reported as non-zero per above), the device will re-try the highest priority configuration.
The default timer for this is 10 minutes and can be set with [timer.port.testbetterinterval](CONFIG-PROPERTIES.md). That timer can be set to zero to disable this re-try.

If the re-try succeeds then SystemAdapterInfo is updated with the previously reported error cleared. The fact that it has failed in the past can be seen from the reported lastFailed timestamp in SystemAdapterInfo being non-zero.

### Handling remote (temporary) failures

There is a set of failures which can be attributed to the controller having issues which does not warrant any churn or fallback on the device. The current cases are:

- the server certificate having expired (or not yet being valid)
- the server responding with a TCP Reset/Connection refused (and proxy is not in the path)

In those cases nim proceeds with the current configuration and assumes that the server will at some point in time be corrected.

## Failure reporting

The device reports the status of all of the device connectivity using [SystemAdapterInfo](../api/proto/info/info.proto). There are two levels of errors:

- A new SystemAdapter configuration was tested, but none of the management ports could be used to connect to the controller. In that case a failure is reported by setting lastError in DevicePortStatus and the currentIndex is set to the currently used DevicePortStatus in the list. Note that lastFailed and lastSucceeded can be used to see if a configuration has succeeded in the past or always failed.
- A particular management port could not be used to reach the controller. In that case the ErrorInfo for the particular DevicePort is set to indicate the error and timestamp.
