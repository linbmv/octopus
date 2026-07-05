package op

type Services struct {
	Settings  *SettingsService
	Users     *UserService
	Channels  *ChannelService
	Groups    *GroupService
	Stats     *StatsService
	RelayLogs *RelayLogService
}

func DefaultServices() Services {
	return Services{
		Settings:  DefaultSettingsService(),
		Users:     DefaultUserService(),
		Channels:  DefaultChannelService(),
		Groups:    DefaultGroupService(),
		Stats:     DefaultStatsService(),
		RelayLogs: DefaultRelayLogService(),
	}
}
