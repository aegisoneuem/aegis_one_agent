package config

type Config struct {
	ServerAddress string
	LogLevel      string
}

func Load() Config {
	// Abhi hardcoded — baad mein file/env-variable se aayega
	return Config{
		ServerAddress: "localhost:50051",
		LogLevel:      "info",
	}
}