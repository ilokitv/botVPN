package config

import (
	"fmt"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// Config содержит настройки всего приложения
type Config struct {
	Bot      BotConfig      `yaml:"bot"`
	Database DatabaseConfig `yaml:"database"`
	Payments PaymentsConfig `yaml:"payments"`
}

// BotConfig содержит настройки Telegram бота
type BotConfig struct {
	Token    string  `yaml:"token"`
	AdminIDs []int64 `yaml:"admin_ids"`
}

// DatabaseConfig содержит настройки базы данных
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// PaymentsConfig содержит настройки платежей
type PaymentsConfig struct {
	Provider string `yaml:"provider"`
}

// GetConnectionString возвращает строку подключения к базе данных
func (dc *DatabaseConfig) GetConnectionString() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		dc.Host, dc.Port, dc.User, dc.Password, dc.DBName, dc.SSLMode)
}

// Load загружает конфигурацию из файла
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Error reading config file: %v", err)
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Printf("Error unmarshaling config: %v", err)
		return nil, err
	}

	return &config, nil
}
