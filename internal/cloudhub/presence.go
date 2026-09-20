package cloudhub

func normalizeHeartbeatStatus(status DeviceStatus) DeviceStatus {
	switch status {
	case DeviceStatusOffline:
		return DeviceStatusOffline
	case DeviceStatusOnline:
		return DeviceStatusOnline
	default:
		return DeviceStatusOnline
	}
}
