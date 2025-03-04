package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/ilokitv/botVPN/internal/config"
	"github.com/ilokitv/botVPN/internal/database"
	"github.com/ilokitv/botVPN/internal/handlers"
	"github.com/ilokitv/botVPN/internal/scheduler"
	"github.com/ilokitv/botVPN/internal/vpn"
)

func main() {
	// Парсим аргументы командной строки
	configPath := flag.String("config", "config.yaml", "путь к файлу конфигурации")
	flag.Parse()

	// Загружаем конфигурацию
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Ошибка загрузки конфигурации: %v", err)
	}

	// Создаем подключение к базе данных
	db, err := database.New(&cfg.Database)
	if err != nil {
		log.Fatalf("Ошибка подключения к базе данных: %v", err)
	}
	defer db.Close()

	// Инициализируем таблицы базы данных
	err = db.InitTables()
	if err != nil {
		log.Fatalf("Ошибка инициализации таблиц базы данных: %v", err)
	}

	// Создаем директорию для хранения конфигураций VPN
	configDir := filepath.Join(".", "vpn_configs")
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		err := os.MkdirAll(configDir, 0755)
		if err != nil {
			log.Fatalf("Ошибка создания директории для конфигураций VPN: %v", err)
		}
	}

	// Инициализируем менеджер VPN
	vpnManager := vpn.NewWireguardManager(configDir)

	// Инициализируем Telegram бота
	bot, err := tgbotapi.NewBotAPI(cfg.Bot.Token)
	if err != nil {
		log.Fatalf("Ошибка инициализации Telegram бота: %v", err)
	}

	log.Printf("Бот запущен: %s", bot.Self.UserName)

	// Инициализируем и запускаем планировщик проверки подписок
	// Проверка будет выполняться каждый час
	subscriptionChecker := scheduler.NewSubscriptionChecker(db, vpnManager, bot, 1*time.Hour)
	subscriptionChecker.Start()
	defer subscriptionChecker.Stop()
	log.Println("Планировщик проверки подписок запущен и будет выполняться каждый час")

	// Создаем обработчик бота
	botHandler := handlers.NewBotHandler(bot, db, vpnManager, cfg)

	// Настраиваем обновления
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60

	// Получаем канал обновлений
	updates := bot.GetUpdatesChan(updateConfig)

	// Канал для сигналов завершения работы
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Обрабатываем обновления
	for {
		select {
		case update := <-updates:
			botHandler.HandleUpdate(update)
		case <-stop:
			log.Println("Завершение работы бота...")
			return
		}
	}
}
