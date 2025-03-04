package handlers

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/crypto/ssh"

	"github.com/ilokitv/botVPN/internal/config"
	"github.com/ilokitv/botVPN/internal/database"
	"github.com/ilokitv/botVPN/internal/models"
	"github.com/ilokitv/botVPN/internal/vpn"
)

// BotHandler обрабатывает взаимодействие с Telegram ботом
type BotHandler struct {
	bot        *tgbotapi.BotAPI
	db         *database.DB
	vpnManager *vpn.WireguardManager
	config     *config.Config
	userStates map[int64]UserState
}

// UserState содержит состояние пользователя в диалоге с ботом
type UserState struct {
	State         string
	Data          map[string]string
	PreviousState string
}

// NewBotHandler создает нового обработчика бота
func NewBotHandler(bot *tgbotapi.BotAPI, db *database.DB, vpnManager *vpn.WireguardManager, cfg *config.Config) *BotHandler {
	return &BotHandler{
		bot:        bot,
		db:         db,
		vpnManager: vpnManager,
		config:     cfg,
		userStates: make(map[int64]UserState),
	}
}

// IsAdmin проверяет, является ли пользователь администратором
func (h *BotHandler) IsAdmin(userID int64) bool {
	for _, adminID := range h.config.Bot.AdminIDs {
		if adminID == userID {
			return true
		}
	}
	return false
}

// HandleUpdate обрабатывает обновление от Telegram
func (h *BotHandler) HandleUpdate(update tgbotapi.Update) {
	// Обрабатываем сообщения
	if update.Message != nil {
		// Проверяем на успешный платеж
		if update.Message.SuccessfulPayment != nil {
			h.handleSuccessfulPayment(update.Message)
			return
		}

		h.handleMessage(update.Message)
		return
	}

	// Обрабатываем обратные вызовы (inline keyboard)
	if update.CallbackQuery != nil {
		h.handleCallbackQuery(update.CallbackQuery)
		return
	}

	// Обрабатываем предварительные запросы на оплату
	if update.PreCheckoutQuery != nil {
		h.handlePreCheckoutQuery(update.PreCheckoutQuery)
		return
	}
}

// handleMessage обрабатывает сообщения от пользователя
func (h *BotHandler) handleMessage(message *tgbotapi.Message) {
	userID := message.From.ID
	// Не используем chatID здесь, но она нужна в некоторых методах
	_ = message.Chat.ID

	// Сохраняем пользователя в базу данных, если это новый пользователь
	user := &models.User{
		TelegramID: userID,
		Username:   message.From.UserName,
		FirstName:  message.From.FirstName,
		LastName:   message.From.LastName,
		IsAdmin:    h.IsAdmin(userID),
	}

	err := h.db.AddUser(user)
	if err != nil {
		log.Printf("Error adding user to database: %v", err)
	}

	// Обрабатываем команды
	if message.IsCommand() {
		h.handleCommand(message)
		return
	}

	// Обрабатываем текст в соответствии с текущим состоянием пользователя
	h.handleStateBasedInput(message)
}

// handleCommand обрабатывает команды бота
func (h *BotHandler) handleCommand(message *tgbotapi.Message) {
	command := message.Command()
	userID := message.From.ID
	chatID := message.Chat.ID
	isAdmin := h.IsAdmin(userID)

	switch command {
	case "start":
		h.handleStartCommand(message)

	case "help":
		h.handleHelpCommand(message)

	case "admin":
		if isAdmin {
			h.showAdminMenu(chatID)
		} else {
			h.sendMessage(chatID, "У вас нет прав администратора.")
		}

	case "my":
		h.handleMySubscriptionsCommand(message)

	case "buy":
		h.handleBuyCommand(message)

	default:
		h.sendMessage(chatID, "Неизвестная команда. Используйте /help для получения списка команд.")
	}
}

// handleStateBasedInput обрабатывает ввод на основе текущего состояния пользователя
func (h *BotHandler) handleStateBasedInput(message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID
	userState, exists := h.userStates[userID]

	// Проверяем, есть ли у сообщения текст для обработки
	if message.Text != "" {
		// Сначала проверяем, не является ли это нажатием на кнопку меню
		if h.handleMenuButtonPress(message) {
			return
		}
	}

	// Если у пользователя нет активного состояния, выходим
	if !exists {
		return
	}

	switch userState.State {
	case "add_server_ip":
		userState.Data["ip"] = message.Text
		userState.State = "add_server_port"
		h.userStates[userID] = userState
		h.sendMessage(chatID, "Введите порт SSH:")

	case "add_server_port":
		_, err := strconv.Atoi(message.Text) // Используем _ вместо port, но проверяем валидность
		if err != nil {
			h.sendMessage(chatID, "Пожалуйста, введите корректный порт (число):")
			return
		}

		// Сохраняем порт в данных состояния
		userState.Data["port"] = message.Text

		// Переходим к следующему шагу
		h.sendMessage(chatID, "Введите имя пользователя SSH:")
		userState.State = "add_server_username"
		h.userStates[userID] = userState

	case "add_server_username":
		userState.Data["username"] = message.Text
		userState.State = "add_server_password"
		h.userStates[userID] = userState
		h.sendMessage(chatID, "Введите пароль SSH:")

	case "add_server_password":
		userState.Data["password"] = message.Text
		userState.State = "add_server_max_clients"
		h.userStates[userID] = userState
		h.sendMessage(chatID, "Введите максимальное количество клиентов для сервера:")

	case "add_server_max_clients":
		maxClients, err := strconv.Atoi(message.Text)
		if err != nil {
			h.sendMessage(chatID, "Пожалуйста, введите корректное число клиентов:")
			return
		}

		// Добавляем сервер в базу данных
		portNum, _ := strconv.Atoi(userState.Data["port"])
		server := &models.Server{
			IP:          userState.Data["ip"],
			Port:        portNum,
			SSHUser:     userState.Data["username"],
			SSHPassword: userState.Data["password"],
			MaxClients:  maxClients,
			IsActive:    true,
		}

		// Предварительная настройка сервера
		h.sendMessage(chatID, "Настраиваю сервер, это может занять некоторое время...")

		err = h.vpnManager.SetupServer(server)
		if err != nil {
			h.sendMessage(chatID, fmt.Sprintf("Ошибка при настройке сервера: %v", err))
			delete(h.userStates, userID)
			return
		}

		err = h.db.AddServer(server)
		if err != nil {
			h.sendMessage(chatID, fmt.Sprintf("Ошибка при добавлении сервера в базу данных: %v", err))
			delete(h.userStates, userID)
			return
		}

		h.sendMessage(chatID, fmt.Sprintf("Сервер успешно добавлен с ID: %d", server.ID))
		delete(h.userStates, userID)

	// Другие состояния для обработки
	case "add_plan_name":
		userState.Data["name"] = message.Text
		userState.State = "add_plan_description"
		h.userStates[userID] = userState
		h.sendMessage(chatID, "Введите описание плана подписки:")

	case "add_plan_description":
		userState.Data["description"] = message.Text
		userState.State = "add_plan_price"
		h.userStates[userID] = userState
		h.sendMessage(chatID, "Введите цену плана подписки:")

	case "add_plan_price":
		_, err := strconv.ParseFloat(message.Text, 64) // Используем _ вместо price, но проверяем валидность
		if err != nil {
			h.sendMessage(chatID, "Пожалуйста, введите корректную цену:")
			return
		}

		// Сохраняем цену в данных состояния
		userState.Data["price"] = message.Text

		// Переходим к следующему шагу
		h.sendMessage(chatID, "Введите длительность плана в днях:")
		userState.State = "add_plan_duration"
		h.userStates[userID] = userState

	case "add_plan_duration":
		duration, err := strconv.Atoi(message.Text)
		if err != nil {
			h.sendMessage(chatID, "Пожалуйста, введите корректную длительность (число дней):")
			return
		}

		// Добавляем план подписки в базу данных
		priceValue, _ := strconv.ParseFloat(userState.Data["price"], 64)
		plan := &models.SubscriptionPlan{
			Name:        userState.Data["name"],
			Description: userState.Data["description"],
			Price:       priceValue,
			Duration:    duration,
			IsActive:    true,
		}

		err = h.db.AddSubscriptionPlan(plan)
		if err != nil {
			h.sendMessage(chatID, fmt.Sprintf("Ошибка при добавлении плана подписки: %v", err))
			delete(h.userStates, userID)
			return
		}

		h.sendMessage(chatID, fmt.Sprintf("План подписки успешно добавлен: %s", plan.Name))
		delete(h.userStates, userID)

		// Возвращаемся к списку планов
		h.listSubscriptionPlans(chatID)

	// Состояния для редактирования плана подписки
	case "edit_plan_name":
		if message.Text != "." {
			userState.Data["new_name"] = message.Text
		} else {
			userState.Data["new_name"] = userState.Data["name"]
		}
		userState.State = "edit_plan_description"
		h.userStates[userID] = userState
		h.sendMessage(chatID, fmt.Sprintf("Введите новое описание плана (или отправьте точку '.' чтобы оставить текущее описание: \n\n%s)", userState.Data["description"]))

	case "edit_plan_description":
		if message.Text != "." {
			userState.Data["new_description"] = message.Text
		} else {
			userState.Data["new_description"] = userState.Data["description"]
		}
		userState.State = "edit_plan_price"
		h.userStates[userID] = userState
		h.sendMessage(chatID, fmt.Sprintf("Введите новую цену плана (или отправьте точку '.' чтобы оставить текущую цену: %s руб.):", userState.Data["price"]))

	case "edit_plan_price":
		var err error

		if message.Text != "." {
			_, err = strconv.ParseFloat(message.Text, 64)
			if err != nil {
				h.sendMessage(chatID, "Пожалуйста, введите корректную цену (число с точкой):")
				return
			}
			userState.Data["new_price"] = message.Text
		} else {
			userState.Data["new_price"] = userState.Data["price"]
		}

		userState.State = "edit_plan_duration"
		h.userStates[userID] = userState
		h.sendMessage(chatID, fmt.Sprintf("Введите новую длительность плана в днях (или отправьте точку '.' чтобы оставить текущую длительность: %s дней):", userState.Data["duration"]))

	case "edit_plan_duration":
		var err error

		if message.Text != "." {
			_, err = strconv.Atoi(message.Text)
			if err != nil {
				h.sendMessage(chatID, "Пожалуйста, введите корректную длительность (целое число дней):")
				return
			}
			userState.Data["new_duration"] = message.Text
		} else {
			userState.Data["new_duration"] = userState.Data["duration"]
		}

		// Переходим к выбору статуса активности
		userState.State = "edit_plan_status"
		h.userStates[userID] = userState

		// Создаем клавиатуру для выбора статуса
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🟢 Активен", "plan_status:active"),
				tgbotapi.NewInlineKeyboardButtonData("🔴 Неактивен", "plan_status:inactive"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("Оставить текущий статус", "plan_status:current"),
			),
		)

		statusMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Выберите статус плана (текущий статус: %s):",
			getStatusText(userState.Data["is_active"] == "true")))
		statusMsg.ReplyMarkup = keyboard
		h.bot.Send(statusMsg)

	case "edit_plan_status":
		// Обработка выбора статуса в handleCallbackQuery
		// Обновляем план подписки в базе данных
		planID, _ := strconv.Atoi(userState.Data["plan_id"])
		newPrice, _ := strconv.ParseFloat(userState.Data["new_price"], 64)
		newDuration, _ := strconv.Atoi(userState.Data["new_duration"])
		isActive := userState.Data["new_is_active"] == "true"

		plan := &models.SubscriptionPlan{
			ID:          planID,
			Name:        userState.Data["new_name"],
			Description: userState.Data["new_description"],
			Price:       newPrice,
			Duration:    newDuration,
			IsActive:    isActive,
		}

		err := h.db.UpdateSubscriptionPlan(plan)
		if err != nil {
			h.sendMessage(chatID, fmt.Sprintf("Ошибка при обновлении плана подписки: %v", err))
			delete(h.userStates, userID)
			return
		}

		h.sendMessage(chatID, fmt.Sprintf("✅ План подписки успешно обновлен: %s", plan.Name))
		delete(h.userStates, userID)

		// Отображаем обновленный план
		h.viewPlanDetails(chatID, planID)

	default:
		// Неизвестное состояние
		delete(h.userStates, userID)
	}
}

// handleCallbackQuery обрабатывает нажатия на инлайн-кнопки
func (h *BotHandler) handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	data := query.Data

	log.Printf("Получен callback: data=%s, от пользователя ID=%d", data, query.From.ID)

	// Отвечаем на запрос обратного вызова
	h.bot.Request(tgbotapi.NewCallback(query.ID, ""))

	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		log.Printf("Некорректный формат колбэка: %s (недостаточно частей)", data)
		return
	}

	action := parts[0]
	log.Printf("Обработка действия: %s, parts=%v", action, parts)

	switch action {
	case "admin_menu":
		h.handleAdminMenuSelection(chatID, parts[1])

	case "server_action":
		if len(parts) < 3 {
			return
		}
		serverID, _ := strconv.Atoi(parts[2])
		h.handleServerAction(chatID, parts[1], serverID)

	case "plan_action":
		if len(parts) < 3 {
			return
		}
		planID, _ := strconv.Atoi(parts[2])
		h.handlePlanAction(chatID, parts[1], planID)

	case "user_action":
		if len(parts) < 3 {
			log.Printf("Некорректный формат для user_action: %s (необходимо 3 части)", data)
			return
		}
		userID, err := strconv.Atoi(parts[2])
		if err != nil {
			log.Printf("Ошибка преобразования ID пользователя: %v", err)
			return
		}
		log.Printf("Вызов handleUserAction с параметрами: action=%s, userID=%d", parts[1], userID)
		h.handleUserAction(chatID, parts[1], userID)

	case "stats_action":
		if len(parts) < 3 {
			return
		}
		param, _ := strconv.Atoi(parts[2])
		h.handleStatsAction(chatID, parts[1], param)

	case "subscription_action":
		if len(parts) < 3 {
			return
		}
		subscriptionID, _ := strconv.Atoi(parts[2])
		h.handleSubscriptionAction(chatID, parts[1], subscriptionID)

	case "buy_plan":
		planID, _ := strconv.Atoi(parts[1])
		userID := query.From.ID
		h.handleBuyPlan(chatID, userID, planID)

	case "show_buy_plans":
		h.listAvailableSubscriptionPlans(chatID)

	case "server_confirm_delete":
		if len(parts) < 2 {
			log.Printf("Некорректный формат для server_confirm_delete: %s (необходимо 2 части)", data)
			return
		}
		serverID, _ := strconv.Atoi(parts[1])
		h.handleServerConfirmDelete(chatID, serverID)

		if strings.HasPrefix(data, "plan_status:") {
			// Обработка выбора статуса плана при редактировании
			status := strings.TrimPrefix(data, "plan_status:")
			userID := query.From.ID
			if userState, ok := h.userStates[userID]; ok && userState.State == "edit_plan_status" {
				switch status {
				case "active":
					userState.Data["new_is_active"] = "true"
				case "inactive":
					userState.Data["new_is_active"] = "false"
				case "current":
					userState.Data["new_is_active"] = userState.Data["is_active"]
				}
				h.userStates[userID] = userState

				// Отправляем подтверждение выбора
				editMsg := tgbotapi.NewEditMessageText(
					query.Message.Chat.ID,
					query.Message.MessageID,
					fmt.Sprintf("Статус плана: %s\n\nСохраняю изменения...",
						getStatusText(userState.Data["new_is_active"] == "true")),
				)
				h.bot.Send(editMsg)

				// Моделируем получение сообщения для обработки в edit_plan_status
				msg := tgbotapi.Message{
					From: &tgbotapi.User{ID: userID},
					Chat: &tgbotapi.Chat{ID: query.Message.Chat.ID},
				}
				h.handleStateBasedInput(&msg)

				return
			}
		}
	}
}

// handlePreCheckoutQuery обрабатывает запросы на оплату
func (h *BotHandler) handlePreCheckoutQuery(query *tgbotapi.PreCheckoutQuery) {
	// Принимаем оплату
	config := tgbotapi.PreCheckoutConfig{
		PreCheckoutQueryID: query.ID,
		OK:                 true,
		ErrorMessage:       "",
	}
	h.bot.Request(config)
}

// Обработчики конкретных команд

// handleStartCommand обрабатывает команду /start
func (h *BotHandler) handleStartCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID

	welcomeText := `
🔒 *Добро пожаловать в VPN бот!*

Этот бот поможет вам приобрести и управлять подписками на VPN-сервис.
Используйте кнопки меню для быстрого доступа к функциям.
`

	if h.IsAdmin(userID) {
		welcomeText += "\nУ вас есть права администратора!"
	}

	h.sendMainMenu(chatID, welcomeText, userID)
}

// sendMainMenu отправляет пользователю главное меню с кнопками
func (h *BotHandler) sendMainMenu(chatID int64, text string, userID int64) {
	// Создаем красивую клавиатуру с основными функциями
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("💰 Купить подписку"),
			tgbotapi.NewKeyboardButton("🔑 Мои подписки"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("ℹ️ Помощь"),
			tgbotapi.NewKeyboardButton("📞 Поддержка"),
		),
	)

	// Для администраторов добавляем отдельную кнопку
	if h.IsAdmin(userID) {
		keyboard.Keyboard = append(keyboard.Keyboard, tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("⚙️ Админ-панель"),
		))
	}

	// Устанавливаем различные параметры меню
	keyboard.ResizeKeyboard = true
	keyboard.OneTimeKeyboard = false
	keyboard.Selective = false

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	h.bot.Send(msg)
}

// handleHelpCommand обрабатывает команду /help
func (h *BotHandler) handleHelpCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID

	helpText := `
*Справка по использованию VPN-бота*

*Основные кнопки меню:*
• 💰 *Купить подписку* - просмотр и покупка доступных тарифных планов
• 🔑 *Мои подписки* - управление вашими активными подписками
• ℹ️ *Помощь* - получение этой справки
• 📞 *Поддержка* - связь с командой поддержки

*Доступные команды:*
• /start - отобразить главное меню бота
• /help - показать эту справку
• /buy - купить подписку на VPN
• /my - просмотреть ваши активные подписки
`

	if h.IsAdmin(userID) {
		helpText += `
*Команды администратора:*
• ⚙️ *Админ-панель* - меню управления ботом
• /admin - открыть панель администратора
`
	}

	msg := tgbotapi.NewMessage(chatID, helpText)
	msg.ParseMode = "Markdown"
	h.bot.Send(msg)
}

// handleMySubscriptionsCommand обрабатывает команду /my
func (h *BotHandler) handleMySubscriptionsCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID

	log.Printf("Обработка команды /my для пользователя %d", userID)

	// Получаем пользователя из базы данных
	user, err := h.db.GetUserByTelegramID(userID)
	if err != nil {
		log.Printf("Ошибка при получении пользователя по TelegramID %d: %v", userID, err)
		h.sendMessage(chatID, "❌ Ошибка при получении информации о пользователе. Пожалуйста, попробуйте позже.")
		return
	}

	log.Printf("Получен пользователь: ID=%d, TelegramID=%d", user.ID, user.TelegramID)

	// Получаем подписки пользователя
	subscriptions, err := h.db.GetSubscriptionsByUserID(user.ID)
	if err != nil {
		log.Printf("Ошибка при получении подписок для пользователя ID=%d: %v", user.ID, err)
		h.sendMessage(chatID, "❌ Ошибка при получении информации о подписках. Пожалуйста, попробуйте позже.")
		return
	}

	log.Printf("Получено %d подписок для пользователя ID=%d", len(subscriptions), user.ID)

	if len(subscriptions) == 0 {
		// Отправляем красивое сообщение с предложением купить подписку
		noSubsMsg := `
*У вас пока нет активных подписок* 🔎

Чтобы начать пользоваться VPN-сервисом:
1️⃣ Нажмите на кнопку *"💰 Купить подписку"*
2️⃣ Выберите подходящий тарифный план
3️⃣ Оплатите подписку через Telegram
4️⃣ Получите доступ к VPN мгновенно!
`
		msg := tgbotapi.NewMessage(chatID, noSubsMsg)
		msg.ParseMode = "Markdown"

		// Добавляем кнопку для быстрого перехода к покупке
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💰 Выбрать план подписки", "show_buy_plans"),
			),
		)
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return
	}

	// Заголовок списка подписок
	headerMsg := fmt.Sprintf("*🔑 Ваши VPN-подписки (%d)*\n", len(subscriptions))
	h.sendMessage(chatID, headerMsg)

	// Определяем, является ли пользователь администратором
	isAdmin := h.IsAdmin(userID)

	// Для каждой подписки формируем отдельную карточку
	for _, subscription := range subscriptions {
		// Получаем информацию о сервере
		server, err := h.db.GetServerByID(subscription.ServerID)
		if err != nil {
			log.Printf("Ошибка при получении сервера ID=%d: %v", subscription.ServerID, err)
			continue
		}

		// Получаем информацию о плане
		plan, err := h.db.GetSubscriptionPlanByID(subscription.PlanID)
		if err != nil {
			// Если не удалось получить план, используем значение по умолчанию
			log.Printf("Ошибка при получении плана подписки ID=%d: %v", subscription.PlanID, err)
			plan = &models.SubscriptionPlan{Name: "План подписки"}
		} else {
			log.Printf("Успешно получен план ID=%d: %s", plan.ID, plan.Name)
		}

		// Выбираем эмодзи в зависимости от статуса
		var statusEmoji, statusText string
		switch subscription.Status {
		case "active":
			statusEmoji = "✅"
			statusText = "Активна"
		case "blocked":
			statusEmoji = "🔒"
			statusText = "Заблокирована"
		case "expired":
			statusEmoji = "⏱️"
			statusText = "Истекла"
		case "revoked":
			statusEmoji = "❌"
			statusText = "Отозвана"
		default:
			statusEmoji = "❓"
			statusText = subscription.Status
		}

		// Вычисляем дни до истечения подписки
		daysLeft := int(subscription.EndDate.Sub(time.Now()).Hours() / 24)
		var daysLeftText string
		if daysLeft > 0 {
			daysLeftText = fmt.Sprintf("🗓️ *Осталось дней:* %d\n", daysLeft)
		} else {
			daysLeftText = "🗓️ *Статус:* Просрочена\n"
		}

		// Формируем красивое сообщение о подписке
		infoMsg := fmt.Sprintf(
			"*VPN-подписка #%d*\n\n"+
				"%s *Статус:* %s\n"+
				"📋 *План:* %s\n"+
				"🌐 *Сервер:* %s\n"+
				"📅 *Действует до:* %s\n"+
				"%s"+
				"📊 *Использовано данных:* %s\n",
			subscription.ID,
			statusEmoji, statusText,
			plan.Name,
			server.IP,
			subscription.EndDate.Format("02.01.2006"),
			daysLeftText,
			formatBytes(subscription.DataUsage),
		)

		// Если есть последнее подключение, добавляем эту информацию
		if subscription.LastConnectionAt != nil && !subscription.LastConnectionAt.IsZero() {
			infoMsg += fmt.Sprintf("🔄 *Последнее подключение:* %s\n",
				subscription.LastConnectionAt.Format("02.01.2006 15:04"))
		}

		// Создаем клавиатуру для этой подписки
		keyboard := tgbotapi.NewInlineKeyboardMarkup()

		// Основные кнопки для всех пользователей
		row := tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📄 Конфигурация", fmt.Sprintf("subscription_action:config:%d", subscription.ID)),
			tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", fmt.Sprintf("subscription_action:stats:%d", subscription.ID)),
		)
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, row)

		// Если пользователь администратор, добавляем кнопки управления
		if isAdmin {
			var adminRow []tgbotapi.InlineKeyboardButton

			// Проверяем текущий статус подписки
			if subscription.Status == "blocked" {
				adminRow = tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔓 Разблокировать", fmt.Sprintf("subscription_action:unblock:%d", subscription.ID)),
				)
			} else {
				adminRow = tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔒 Блокировать", fmt.Sprintf("subscription_action:block:%d", subscription.ID)),
				)
			}
			keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, adminRow)
		}

		msg := tgbotapi.NewMessage(chatID, infoMsg)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
	}

	// Добавляем кнопку для покупки новой подписки после списка
	if len(subscriptions) > 0 {
		buyMoreMsg := "*Хотите добавить еще одну подписку?*"
		msg := tgbotapi.NewMessage(chatID, buyMoreMsg)
		msg.ParseMode = "Markdown"

		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💰 Купить еще подписку", "show_buy_plans"),
			),
		)
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
	}
}

// handleBuyCommand обрабатывает команду /buy
func (h *BotHandler) handleBuyCommand(message *tgbotapi.Message) {
	chatID := message.Chat.ID
	h.listAvailableSubscriptionPlans(chatID)
}

// showStatsMenu отображает меню статистики
func (h *BotHandler) showStatsMenu(chatID int64) {
	text := "Меню статистики. Выберите действие:"

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Общая статистика", "stats_action:overview:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Статистика доходов", "stats_action:revenue:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Статистика серверов", "stats_action:servers:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Назад", "admin_menu:main"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = keyboard

	h.bot.Send(msg)
}

// handleStatsAction обрабатывает действия в меню статистики
func (h *BotHandler) handleStatsAction(chatID int64, action string, param int) {
	switch action {
	case "overview":
		h.showSystemStats(chatID)

	case "revenue":
		h.showRevenueStats(chatID)

	case "servers":
		h.showServerStats(chatID)

	default:
		h.sendMessage(chatID, "Неизвестное действие. Пожалуйста, выберите действие из меню.")
	}
}

// showSystemStats отображает общую статистику системы
func (h *BotHandler) showSystemStats(chatID int64) {
	// Получаем статистику системы
	stats, err := h.db.GetSystemStats()
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при получении статистики системы: %v", err))
		return
	}

	// Вычисляем процент загрузки серверов
	var loadPercentage float64
	if stats.TotalCapacity > 0 {
		loadPercentage = float64(stats.TotalClients) * 100 / float64(stats.TotalCapacity)
	}

	text := fmt.Sprintf(
		"📊 *Общая статистика системы*\n\n"+
			"👥 *Пользователи:*\n"+
			"- Всего пользователей: %d\n"+
			"- Новые пользователи (7 дней): %d\n\n"+
			"🔑 *Подписки:*\n"+
			"- Активных подписок: %d\n"+
			"- Новые подписки (7 дней): %d\n\n"+
			"💰 *Доходы:*\n"+
			"- Общий доход: %.2f руб.\n"+
			"- Доход за 30 дней: %.2f руб.\n\n"+
			"🖥 *Серверы:*\n"+
			"- Активных серверов: %d\n"+
			"- Подключено клиентов: %d\n"+
			"- Общая вместимость: %d\n"+
			"- Загрузка серверов: %.1f%%",
		stats.TotalUsers,
		stats.NewUsers7Days,
		stats.ActiveSubscriptions,
		stats.NewSubscriptions7Days,
		stats.TotalRevenue,
		stats.MonthlyRevenue,
		stats.TotalServers,
		stats.TotalClients,
		stats.TotalCapacity,
		loadPercentage,
	)

	// Добавляем кнопку возврата
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Назад", "admin_menu:stats"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = keyboard

	h.bot.Send(msg)
}

// showRevenueStats отображает статистику доходов
func (h *BotHandler) showRevenueStats(chatID int64) {
	// TODO: Реализовать более подробную статистику доходов
	// Пока просто перенаправляем на общую статистику
	h.showSystemStats(chatID)
}

// showServerStats отображает статистику по серверам
func (h *BotHandler) showServerStats(chatID int64) {
	// Получаем список серверов
	servers, err := h.db.GetAllServers()
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при получении списка серверов: %v", err))
		return
	}

	if len(servers) == 0 {
		h.sendMessage(chatID, "Серверы не найдены.")
		return
	}

	text := "📊 *Статистика серверов*\n\n"

	for _, server := range servers {
		var loadPercentage float64
		if server.MaxClients > 0 {
			loadPercentage = float64(server.CurrentClients) * 100 / float64(server.MaxClients)
		}

		statusEmoji := "✅"
		if !server.IsActive {
			statusEmoji = "❌"
		}

		text += fmt.Sprintf(
			"🖥 *Сервер #%d* %s\n"+
				"- IP: `%s`\n"+
				"- Клиенты: %d/%d (%.1f%%)\n\n",
			server.ID,
			statusEmoji,
			server.IP,
			server.CurrentClients,
			server.MaxClients,
			loadPercentage,
		)
	}

	// Добавляем кнопку возврата
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Назад", "admin_menu:stats"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = keyboard

	h.bot.Send(msg)
}

// handleSuccessfulPayment обрабатывает успешный платеж через Telegram Stars
func (h *BotHandler) handleSuccessfulPayment(message *tgbotapi.Message) {
	userID := message.From.ID
	chatID := message.Chat.ID
	payment := message.SuccessfulPayment

	log.Printf("Получен успешный платеж от пользователя %d: %+v", userID, payment)

	// Извлекаем ID плана из InvoicePayload
	parts := strings.Split(payment.InvoicePayload, ":")
	if len(parts) != 2 || parts[0] != "plan" {
		h.sendMessage(chatID, "Ошибка при обработке платежа: неверный формат данных.")
		return
	}

	planID, err := strconv.Atoi(parts[1])
	if err != nil {
		h.sendMessage(chatID, "Ошибка при обработке платежа: неверный ID плана.")
		return
	}

	// Получаем информацию о плане
	plan, err := h.db.GetSubscriptionPlanByID(planID)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при получении информации о плане: %v", err))
		return
	}

	// Проверяем доступность серверов
	servers, err := h.db.GetAllServers()
	if err != nil {
		h.sendMessage(chatID, "Ошибка при проверке доступности серверов. Пожалуйста, попробуйте позже.")
		return
	}

	var availableServer *models.Server
	for _, server := range servers {
		if server.IsActive && server.CurrentClients < server.MaxClients {
			availableServer = &server
			break
		}
	}

	if availableServer == nil {
		h.sendMessage(chatID, "К сожалению, в данный момент нет доступных серверов. Пожалуйста, попробуйте позже.")
		return
	}

	// Получаем пользователя
	user, err := h.db.GetUserByTelegramID(userID)
	if err != nil {
		h.sendMessage(chatID, "Ошибка при получении информации о пользователе. Пожалуйста, попробуйте позже.")
		return
	}

	// Создаем подписку
	startDate := time.Now()
	endDate := startDate.AddDate(0, 0, plan.Duration) // Используем длительность из плана

	subscription := &models.Subscription{
		UserID:    user.ID,
		ServerID:  availableServer.ID,
		PlanID:    planID,
		StartDate: startDate,
		EndDate:   endDate,
		Status:    "active",
	}

	// Проверяем, что сервер правильно настроен
	err = h.vpnManager.SetupServer(availableServer)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при настройке сервера VPN: %v", err))
		return
	}

	// Генерируем конфигурационный файл
	configPath, err := h.vpnManager.CreateClientConfig(availableServer, fmt.Sprintf("user_%d", user.ID))
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при создании конфигурации VPN: %v", err))
		return
	}

	subscription.ConfigFilePath = configPath

	// Сохраняем подписку в базу данных
	err = h.db.AddSubscription(subscription)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при создании подписки: %v", err))
		return
	}

	// Создаем запись о платеже
	paymentRecord := &models.Payment{
		UserID:         user.ID,
		SubscriptionID: subscription.ID,
		Amount:         float64(payment.TotalAmount) / 100.0, // Переводим из копеек в рубли
		PaymentMethod:  "telegram_stars",
		PaymentID:      payment.TelegramPaymentChargeID,
		Status:         "completed",
	}

	err = h.db.AddPayment(paymentRecord)
	if err != nil {
		log.Printf("Ошибка при сохранении платежа в базу данных: %v", err)
	}

	// Отправляем файл конфигурации пользователю
	configFile := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(configPath))
	configFile.Caption = "Вот ваш файл конфигурации VPN. Инструкция по установке в следующем сообщении."

	_, err = h.bot.Send(configFile)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при отправке файла конфигурации: %v", err))
		return
	}

	// Отправляем инструкцию
	instructions := `
*Инструкция по настройке VPN:*

1. Скачайте и установите клиент AmneziaVPN:
   - для Windows: https://github.com/amnezia-vpn/amnezia-client/releases/download/4.8.3.1/AmneziaVPN_4.8.3.1_x64.exe
   - для MacOS: https://github.com/amnezia-vpn/amnezia-client/releases/download/4.8.3.1/AmneziaVPN_4.8.3.1_macos.dmg
   - для iOS: https://apps.apple.com/us/app/amneziavpn/id1600529900
   - для Android: https://play.google.com/store/apps/details?id=org.amnezia.vpn

2. Откройте клиент AmneziaVPN
3. Импортируйте полученный файл конфигурации
4. Активируйте подключение

Готово! Теперь ваш трафик защищен VPN.
`

	instrMsg := tgbotapi.NewMessage(chatID, instructions)
	instrMsg.ParseMode = "Markdown"

	h.bot.Send(instrMsg)

	// Отправляем сообщение о успешной покупке
	successMsg := fmt.Sprintf(
		"✅ *Подписка успешно оформлена!*\n\n"+
			"План: %s\n"+
			"Срок действия: %d дней\n"+
			"Дата начала: %s\n"+
			"Дата окончания: %s\n\n"+
			"Спасибо за покупку!",
		plan.Name,
		plan.Duration,
		startDate.Format("02.01.2006"),
		endDate.Format("02.01.2006"),
	)

	msg := tgbotapi.NewMessage(chatID, successMsg)
	msg.ParseMode = "Markdown"
	h.bot.Send(msg)
}

// handleMenuButtonPress обрабатывает нажатия на кнопки основного меню
func (h *BotHandler) handleMenuButtonPress(message *tgbotapi.Message) bool {
	text := message.Text
	chatID := message.Chat.ID
	userID := message.From.ID

	switch text {
	case "💰 Купить подписку":
		h.handleBuyCommand(message)
		return true

	case "🔑 Мои подписки":
		h.handleMySubscriptionsCommand(message)
		return true

	case "ℹ️ Помощь":
		h.handleHelpCommand(message)
		return true

	case "📞 Поддержка":
		supportMsg := `
*Поддержка VPN-сервиса*

Если у вас возникли вопросы или проблемы с использованием нашего VPN:

1. Опишите вашу проблему подробно
2. Укажите, какую подписку вы используете
3. По возможности, приложите скриншоты ошибок

Наша команда поддержки ответит вам в кратчайшие сроки!
`
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL("📞 Написать в поддержку", "https://t.me/Demokrat_repablick"),
			),
		)

		msg := tgbotapi.NewMessage(chatID, supportMsg)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return true

	case "⚙️ Админ-панель":
		// Проверяем, является ли пользователь администратором
		if h.IsAdmin(userID) {
			h.showAdminMenu(chatID)
			return true
		} else {
			h.sendMessage(chatID, "У вас нет прав администратора.")
			return true
		}
	}

	return false
}

// sendMessage отправляет сообщение пользователю
func (h *BotHandler) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	_, err := h.bot.Send(msg)
	if err != nil {
		log.Printf("Ошибка при отправке сообщения: %v", err)
	}
}

// formatBytes преобразует байты в удобный для чтения формат (КБ, МБ, ГБ)
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// listAvailableSubscriptionPlans отображает список доступных планов подписки для покупки
func (h *BotHandler) listAvailableSubscriptionPlans(chatID int64) {
	// Получаем список активных планов подписки
	plans, err := h.db.GetAllSubscriptionPlans()
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении списка планов: %v", err))
		h.bot.Send(msg)
		return
	}

	// Если нет доступных планов
	if len(plans) == 0 {
		msg := tgbotapi.NewMessage(chatID, "В настоящее время нет доступных планов подписки. Пожалуйста, попробуйте позже.")
		h.bot.Send(msg)
		return
	}

	// Отправляем сообщение с заголовком
	headerMsg := `
*💰 Выберите план подписки*

Ниже представлены доступные тарифные планы для VPN-подключения.
Выберите подходящий вариант и нажмите на кнопку для оформления подписки.
`
	msg := tgbotapi.NewMessage(chatID, headerMsg)
	msg.ParseMode = "Markdown"
	h.bot.Send(msg)

	// Отправляем карточку для каждого плана
	for _, plan := range plans {
		// Пропускаем неактивные планы
		if !plan.IsActive {
			continue
		}

		// Создаем красивое сообщение с описанием плана
		planMsg := fmt.Sprintf(
			"*%s*\n\n"+
				"%s\n\n"+
				"💰 *Цена:* %.2f руб.\n"+
				"⏳ *Длительность:* %d дней\n"+
				"💵 *Цена за день:* %.2f руб.",
			plan.Name,
			plan.Description,
			plan.Price,
			plan.Duration,
			plan.Price/float64(plan.Duration),
		)

		// Создаем инлайн-кнопку для покупки
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("💳 Купить", fmt.Sprintf("buy_plan:%d", plan.ID)),
			),
		)

		planMsgConfig := tgbotapi.NewMessage(chatID, planMsg)
		planMsgConfig.ParseMode = "Markdown"
		planMsgConfig.ReplyMarkup = keyboard

		h.bot.Send(planMsgConfig)
	}

	// Добавляем кнопку для возврата в меню
	footerMsg := "*Остались вопросы?*\nСвяжитесь с нашей технической поддержкой."
	footerMsgConfig := tgbotapi.NewMessage(chatID, footerMsg)
	footerMsgConfig.ParseMode = "Markdown"

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("📞 Поддержка", "https://t.me/Demokrat_repablick"),
		),
	)
	footerMsgConfig.ReplyMarkup = keyboard

	h.bot.Send(footerMsgConfig)
}

// handleBuyPlan обрабатывает покупку выбранного плана подписки
func (h *BotHandler) handleBuyPlan(chatID int64, userID int64, planID int) {
	// Получаем информацию о плане
	plan, err := h.db.GetSubscriptionPlanByID(planID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении информации о плане: %v", err))
		h.bot.Send(msg)
		return
	}

	// Проверяем, что план активен
	if !plan.IsActive {
		msg := tgbotapi.NewMessage(chatID, "Выбранный план недоступен для покупки.")
		h.bot.Send(msg)
		return
	}

	// Проверяем доступность серверов перед оформлением платежа
	servers, err := h.db.GetAllServers()
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Ошибка при проверке доступности серверов. Пожалуйста, попробуйте позже.")
		h.bot.Send(msg)
		return
	}

	var availableServer *models.Server
	for _, server := range servers {
		if server.IsActive && server.CurrentClients < server.MaxClients {
			availableServer = &server
			break
		}
	}

	if availableServer == nil {
		msg := tgbotapi.NewMessage(chatID, "К сожалению, в данный момент нет доступных серверов. Пожалуйста, попробуйте позже.")
		h.bot.Send(msg)
		return
	}

	// Создаем платежный инвойс
	priceInPennies := int(plan.Price * 100) // Переводим в копейки
	invoice := tgbotapi.NewInvoice(
		chatID,
		fmt.Sprintf("VPN-подписка: %s", plan.Name),
		fmt.Sprintf("Подписка на VPN-сервис длительностью %d дней", plan.Duration),
		fmt.Sprintf("plan:%d", planID), // Payload для идентификации плана
		h.config.Payments.Provider,
		"RUB", // Валюта
		"RUB", // Валюта параметра провайдера
		[]tgbotapi.LabeledPrice{
			{
				Label:  plan.Name,
				Amount: priceInPennies,
			},
		},
	)

	// Настраиваем дополнительные параметры инвойса
	invoice.PhotoURL = "https://www.example.com/vpn-logo.jpg" // Опционально: URL изображения
	invoice.NeedName = true
	invoice.NeedEmail = true
	invoice.SendEmailToProvider = true
	invoice.IsFlexible = false
	invoice.DisableNotification = false

	// Отправляем запрос на оплату
	_, err = h.bot.Send(invoice)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при создании счета для оплаты: %v", err))
		h.bot.Send(msg)
		return
	}

	// Сообщение с инструкцией по оплате
	paymentInstructions := `
*Инструкция по оплате:*

1. Нажмите кнопку "Оплатить" в отправленном счете
2. Выберите способ оплаты
3. Следуйте инструкциям для завершения оплаты
4. После успешной оплаты вы получите конфигурационный файл и инструкции по настройке VPN

В случае возникновения проблем с оплатой, обратитесь в службу поддержки.
`
	instructionMsg := tgbotapi.NewMessage(chatID, paymentInstructions)
	instructionMsg.ParseMode = "Markdown"
	h.bot.Send(instructionMsg)
}

// showAdminMenu отображает меню администратора
func (h *BotHandler) showAdminMenu(chatID int64) {
	text := "🔧 *Меню администратора*\n\nВыберите действие:"

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🖥️ Управление серверами", "admin_menu:servers"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📑 Управление планами", "admin_menu:plans"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👥 Управление пользователями", "admin_menu:users"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", "admin_menu:stats"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard

	h.bot.Send(msg)
}

// handleAdminMenuSelection обрабатывает выбор в меню администратора
func (h *BotHandler) handleAdminMenuSelection(chatID int64, selection string) {
	switch selection {
	case "main":
		// Возвращаемся в главное меню администратора
		h.showAdminMenu(chatID)

	case "servers":
		// Показываем список серверов
		servers, err := h.db.GetAllServers()
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении списка серверов: %v", err))
			h.bot.Send(msg)
			return
		}

		if len(servers) == 0 {
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("➕ Добавить сервер", "server_action:add:0"),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
				),
			)

			msg := tgbotapi.NewMessage(chatID, "Серверы не найдены. Добавьте новый сервер.")
			msg.ReplyMarkup = keyboard
			h.bot.Send(msg)
			return
		}

		// Отправляем заголовок
		headerMsg := tgbotapi.NewMessage(chatID, "*Список серверов*\n\nВыберите сервер для управления:")
		headerMsg.ParseMode = "Markdown"
		h.bot.Send(headerMsg)

		// Отправляем информацию о каждом сервере
		for _, server := range servers {
			status := "🟢 Активен"
			if !server.IsActive {
				status = "🔴 Неактивен"
			}

			serverMsg := fmt.Sprintf(
				"*Сервер #%d*\n"+
					"IP: `%s:%d`\n"+
					"Клиенты: %d / %d\n"+
					"Статус: %s",
				server.ID,
				server.IP,
				server.Port,
				server.CurrentClients,
				server.MaxClients,
				status,
			)

			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("📝 Редактировать", fmt.Sprintf("server_action:edit:%d", server.ID)),
					tgbotapi.NewInlineKeyboardButtonData("🔍 Детали", fmt.Sprintf("server_action:view:%d", server.ID)),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("server_action:delete:%d", server.ID)),
				),
			)

			msg := tgbotapi.NewMessage(chatID, serverMsg)
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = keyboard
			h.bot.Send(msg)
		}

		// Добавляем кнопки для создания нового сервера и возврата в меню
		footerKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("➕ Добавить сервер", "server_action:add:0"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
			),
		)

		footerMsg := tgbotapi.NewMessage(chatID, "Действия с серверами:")
		footerMsg.ReplyMarkup = footerKeyboard
		h.bot.Send(footerMsg)

	case "plans":
		// Показываем список планов подписки
		h.listSubscriptionPlans(chatID)

	case "users":
		// Показываем список пользователей
		users, err := h.db.GetAllUsers()
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении списка пользователей: %v", err))
			h.bot.Send(msg)
			return
		}

		// Добавляем отладочный вывод
		log.Printf("Найдено пользователей в базе: %d", len(users))
		for i, user := range users {
			log.Printf("Пользователь %d: ID=%d, TelegramID=%d, Username=%s, IsAdmin=%v",
				i+1, user.ID, user.TelegramID, user.Username, user.IsAdmin)
		}

		if len(users) == 0 {
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
				),
			)
			msg := tgbotapi.NewMessage(chatID, "Список пользователей пуст")
			msg.ReplyMarkup = keyboard
			h.bot.Send(msg)
			return
		}
		headerMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("*Список пользователей (%d всего)*\n\nНиже будут показаны все пользователи. Пожалуйста, дождитесь загрузки всех сообщений:", len(users)))
		headerMsg.ParseMode = "Markdown"
		h.bot.Send(headerMsg)

		// Отправляем информацию о каждом пользователе (ограничиваем вывод 10 пользователями)
		count := 0
		for _, user := range users {
			// Получаем статистику пользователя
			stats, err := h.db.GetUserStats(user.ID)
			if err != nil {
				log.Printf("Ошибка при получении статистики пользователя #%d: %v", user.ID, err)
				continue
			}

			admin := ""
			if user.IsAdmin {
				admin = "👑 Администратор"
			}

			name := user.Username
			if name == "" {
				name = fmt.Sprintf("%s %s", user.FirstName, user.LastName)
			}

			// Добавляем порядковый номер в сообщение для лучшей видимости
			userMsg := fmt.Sprintf(
				"*Пользователь #%d (№%d из %d)*\n"+
					"Имя: `%s`\n"+
					"Telegram ID: `%d`\n"+
					"Дата регистрации: `%s`\n"+
					"Активных подписок: `%d`\n"+
					"Всего подписок: `%d`\n"+
					"Использовано данных: `%.2f GB`\n"+
					"Сумма платежей: `%.2f ₽`\n"+
					"%s",
				user.ID, count+1, len(users), name, user.TelegramID,
				user.CreatedAt.Format("02.01.2006"),
				stats.ActiveSubscriptionsCount,
				stats.SubscriptionsCount,
				float64(stats.TotalDataUsage)/(1024*1024*1024), // Конвертируем байты в GB
				stats.TotalPayments,
				admin)
			fmt.Println(user.ID, count)
			var keyboard tgbotapi.InlineKeyboardMarkup
			if user.IsAdmin {
				keyboard = tgbotapi.NewInlineKeyboardMarkup(
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("🔍 Подписки", fmt.Sprintf("user_action:subscriptions:%d", user.ID)),
					),
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("❌ Снять админа", fmt.Sprintf("user_action:remove_admin:%d", user.ID)),
					),
				)
			} else {
				keyboard = tgbotapi.NewInlineKeyboardMarkup(
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("🔍 Подписки", fmt.Sprintf("user_action:subscriptions:%d", user.ID)),
					),
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("👑 Сделать админом", fmt.Sprintf("user_action:make_admin:%d", user.ID)),
					),
				)
			}

			msg := tgbotapi.NewMessage(chatID, userMsg)
			msg.ParseMode = "Markdown"
			msg.ReplyMarkup = keyboard

			// Добавляем задержку перед отправкой следующего сообщения (1 секунда)
			time.Sleep(1000 * time.Millisecond)

			// Перехватываем возможные ошибки при отправке сообщений
			sentMsg, err := h.bot.Send(msg)
			if err != nil {
				log.Printf("ОШИБКА при отправке сообщения для пользователя %s (ID=%d): %v",
					name, user.ID, err)
				continue
			}

			// Дополнительный отладочный вывод после отправки сообщения
			log.Printf("Отправлено сообщение для пользователя %s (ID=%d), IsAdmin=%v, MessageID=%d",
				name, user.ID, user.IsAdmin, sentMsg.MessageID)

			count++
		}

		// Добавляем кнопку для возврата в меню
		footerKeyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
			),
		)

		footerMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ *Список завершен*\nВсего показано пользователей: *%d*", count))
		footerMsg.ParseMode = "Markdown"
		footerMsg.ReplyMarkup = footerKeyboard
		h.bot.Send(footerMsg)

	case "stats":
		// Показываем меню статистики
		h.showStatsMenu(chatID)
	}
}

// listSubscriptionPlans отображает список планов подписки для администратора
func (h *BotHandler) listSubscriptionPlans(chatID int64) {
	// Получаем все планы подписки
	plans, err := h.db.GetAllSubscriptionPlans()
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении списка планов: %v", err))
		h.bot.Send(msg)
		return
	}

	if len(plans) == 0 {
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("➕ Добавить план", "plan_action:add:0"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
			),
		)

		msg := tgbotapi.NewMessage(chatID, "Планы подписки не найдены. Добавьте новый план.")
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return
	}

	// Отправляем заголовок
	headerMsg := tgbotapi.NewMessage(chatID, "*Список планов подписки*\n\nВыберите план для управления:")
	headerMsg.ParseMode = "Markdown"
	h.bot.Send(headerMsg)

	// Отправляем информацию о каждом плане
	for _, plan := range plans {
		status := "🟢 Активен"
		if !plan.IsActive {
			status = "🔴 Неактивен"
		}

		planMsg := fmt.Sprintf(
			"*%s*\n"+
				"%s\n"+
				"Цена: %.2f руб.\n"+
				"Длительность: %d дней\n"+
				"Статус: %s",
			plan.Name,
			plan.Description,
			plan.Price,
			plan.Duration,
			status,
		)

		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📝 Редактировать", fmt.Sprintf("plan_action:edit:%d", plan.ID)),
				tgbotapi.NewInlineKeyboardButtonData("🔍 Детали", fmt.Sprintf("plan_action:view:%d", plan.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("plan_action:delete:%d", plan.ID)),
			),
		)

		msg := tgbotapi.NewMessage(chatID, planMsg)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
	}

	// Добавляем кнопки для создания нового плана и возврата в меню
	footerKeyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Добавить план", "plan_action:add:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:main"),
		),
	)

	footerMsg := tgbotapi.NewMessage(chatID, "Действия с планами:")
	footerMsg.ReplyMarkup = footerKeyboard
	h.bot.Send(footerMsg)
}

// viewPlanDetails отображает подробную информацию о плане подписки
func (h *BotHandler) viewPlanDetails(chatID int64, planID int) {
	// Получаем информацию о плане
	plan, err := h.db.GetSubscriptionPlanByID(planID)
	if err != nil {
		h.sendMessage(chatID, fmt.Sprintf("Ошибка при получении информации о плане: %v", err))
		return
	}

	if plan == nil {
		h.sendMessage(chatID, "План подписки не найден.")
		return
	}

	// Получаем количество активных подписок на этот план
	// Предполагаем, что у нас нет метода GetActiveSubscriptionCountByPlanID,
	// поэтому будем просто показывать "Недоступно"
	activeSubscriptions := "Недоступно"

	// Получаем общее количество подписок на этот план
	// Предполагаем, что у нас нет метода GetTotalSubscriptionCountByPlanID,
	// поэтому будем просто показывать "Недоступно"
	totalSubscriptions := "Недоступно"

	status := "🟢 Активен"
	if !plan.IsActive {
		status = "🔴 Неактивен"
	}

	// Формируем сообщение с подробной информацией
	planMsg := fmt.Sprintf(
		"*Детали плана подписки*\n\n"+
			"*ID:* `%d`\n"+
			"*Название:* %s\n"+
			"*Описание:* %s\n"+
			"*Цена:* %.2f руб.\n"+
			"*Длительность:* %d дней\n"+
			"*Статус:* %s\n"+
			"*Активных подписок:* %s\n"+
			"*Всего подписок:* %s\n"+
			"*Создан:* %s\n"+
			"*Обновлен:* %s",
		plan.ID,
		plan.Name,
		plan.Description,
		plan.Price,
		plan.Duration,
		status,
		activeSubscriptions,
		totalSubscriptions,
		plan.CreatedAt.Format("02.01.2006 15:04:05"),
		plan.UpdatedAt.Format("02.01.2006 15:04:05"),
	)

	// Кнопки для управления планом
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📝 Редактировать", fmt.Sprintf("plan_action:edit:%d", plan.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("plan_action:delete:%d", plan.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 К списку планов", "admin_menu:plans"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, planMsg)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	h.bot.Send(msg)
}

// handlePlanAction обрабатывает действия с планами подписки
func (h *BotHandler) handlePlanAction(chatID int64, action string, planID int) {
	switch action {
	case "view":
		// Показываем детали плана
		h.viewPlanDetails(chatID, planID)

	case "edit":
		// Получаем план из базы данных
		plan, err := h.db.GetSubscriptionPlanByID(planID)
		if err != nil {
			h.sendMessage(chatID, fmt.Sprintf("Ошибка при получении плана: %v", err))
			return
		}

		if plan == nil {
			h.sendMessage(chatID, "План подписки не найден.")
			return
		}

		// Сохраняем текущие значения плана в состоянии пользователя
		userState := UserState{
			State: "edit_plan_name",
			Data: map[string]string{
				"plan_id":     strconv.Itoa(plan.ID),
				"name":        plan.Name,
				"description": plan.Description,
				"price":       fmt.Sprintf("%.2f", plan.Price),
				"duration":    strconv.Itoa(plan.Duration),
				"is_active":   strconv.FormatBool(plan.IsActive),
			},
		}
		h.userStates[chatID] = userState

		// Отправляем сообщение с текущими значениями плана
		msg := fmt.Sprintf("📝 *Редактирование плана подписки*\n\n"+
			"*Текущие данные:*\n"+
			"*Название:* %s\n"+
			"*Описание:* %s\n"+
			"*Цена:* %.2f руб.\n"+
			"*Длительность:* %d дней\n"+
			"*Статус:* %s %s\n\n"+
			"Введите новое название плана (или отправьте точку '.' чтобы оставить текущее название):",
			plan.Name,
			plan.Description,
			plan.Price,
			plan.Duration,
			getStatusEmoji(plan.IsActive),
			getStatusText(plan.IsActive))

		// Добавляем кнопку отмены
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ Отменить редактирование", "plan:view:"+strconv.Itoa(planID)),
			),
		)

		msgConfig := tgbotapi.NewMessage(chatID, msg)
		msgConfig.ParseMode = "Markdown"
		msgConfig.ReplyMarkup = keyboard
		h.bot.Send(msgConfig)

	case "delete":
		// Запрашиваем подтверждение удаления плана
		plan, err := h.db.GetSubscriptionPlanByID(planID)
		if err != nil {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при получении информации о плане: %v", err))
			h.bot.Send(msg)
			return
		}

		if plan == nil {
			msg := tgbotapi.NewMessage(chatID, "План подписки не найден.")
			h.bot.Send(msg)
			return
		}

		confirmMsg := fmt.Sprintf(
			"Вы действительно хотите удалить план *%s*?\n\n"+
				"⚠️ Внимание: Это действие не повлияет на существующие подписки, но сделает план недоступным для покупки новым пользователям.",
			plan.Name,
		)

		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Да, удалить", fmt.Sprintf("plan_action:confirm_delete:%d", planID)),
				tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", "admin_menu:plans"),
			),
		)

		msg := tgbotapi.NewMessage(chatID, confirmMsg)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)

	case "confirm_delete":
		// Удаляем план подписки
		if err := h.db.DeleteSubscriptionPlan(planID); err != nil {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка при удалении плана: %v", err))
			h.bot.Send(msg)
			return
		}

		msg := tgbotapi.NewMessage(chatID, "✅ План подписки успешно удален.")
		h.bot.Send(msg)

		// Возвращаемся к списку планов
		h.listSubscriptionPlans(chatID)

	case "add":
		// Начинаем процесс добавления нового плана
		userState := UserState{
			State: "add_plan_name",
			Data:  make(map[string]string),
		}
		h.userStates[chatID] = userState

		// Добавляем кнопку отмены
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("❌ Отменить", "admin_menu:plans"),
			),
		)

		msgConfig := tgbotapi.NewMessage(chatID, "➕ *Добавление нового плана подписки*\n\nВведите название плана:")
		msgConfig.ParseMode = "Markdown"
		msgConfig.ReplyMarkup = keyboard
		h.bot.Send(msgConfig)

	default:
		msg := tgbotapi.NewMessage(chatID, "Неизвестное действие с планом.")
		h.bot.Send(msg)
	}
}

// handleServerAction обрабатывает действия с серверами
func (h *BotHandler) handleServerAction(chatID int64, action string, serverID int) {
	log.Printf("Обработка действия с сервером: %s для сервера #%d", action, serverID)

	var responseText string

	switch action {
	case "add":
		// Запускаем процесс добавления сервера
		h.startServerAddition(chatID)
		return

	case "view":
		// Получаем детальную информацию о сервере
		server, err := h.db.GetServerByID(serverID)
		if err != nil {
			responseText = fmt.Sprintf("Ошибка: не удалось найти сервер #%d", serverID)
			break
		}

		// Формируем сообщение с информацией о сервере
		responseText = fmt.Sprintf("🖥️ *Информация о сервере #%d*\n\n", server.ID)
		responseText += fmt.Sprintf("IP: `%s`\n", server.IP)
		responseText += fmt.Sprintf("Порт: `%d`\n", server.Port)
		responseText += fmt.Sprintf("SSH пользователь: `%s`\n", server.SSHUser)
		responseText += fmt.Sprintf("Максимум клиентов: `%d`\n", server.MaxClients)
		responseText += fmt.Sprintf("Текущих клиентов: `%d`\n", server.CurrentClients)
		responseText += fmt.Sprintf("Статус: %s\n", getStatusEmoji(server.IsActive))
		responseText += fmt.Sprintf("Создан: `%s`\n", server.CreatedAt.Format("02.01.2006 15:04:05"))
		responseText += fmt.Sprintf("Обновлен: `%s`\n", server.UpdatedAt.Format("02.01.2006 15:04:05"))

		// Создаем клавиатуру с кнопками действий
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔍 Проверить доступность", fmt.Sprintf("server_action:check:%d", server.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📝 Редактировать", fmt.Sprintf("server_action:edit:%d", server.ID)),
				tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("server_action:delete:%d", server.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к списку серверов", "admin_menu:servers"),
			),
		)

		msg := tgbotapi.NewMessage(chatID, responseText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return

	case "check":
		// Проверяем доступность сервера
		h.checkServerAvailability(chatID, serverID)
		return

	case "edit":
		// Получаем информацию о сервере для редактирования
		server, err := h.db.GetServerByID(serverID)
		if err != nil {
			responseText = fmt.Sprintf("Ошибка: не удалось найти сервер #%d", serverID)
			break
		}

		// Сохраняем ID сервера в сессии пользователя
		h.userStates[chatID] = UserState{
			State: "editing_server",
			Data: map[string]string{
				"server_id": strconv.Itoa(serverID),
			},
		}

		// Формируем сообщение для редактирования
		responseText = fmt.Sprintf("📝 *Редактирование сервера #%d*\n\n", server.ID)
		responseText += "Выберите, что хотите изменить:\n\n"
		responseText += fmt.Sprintf("1. IP: `%s`\n", server.IP)
		responseText += fmt.Sprintf("2. Порт: `%d`\n", server.Port)
		responseText += fmt.Sprintf("3. SSH пользователь: `%s`\n", server.SSHUser)
		responseText += fmt.Sprintf("4. SSH пароль: `%s`\n", maskPassword(server.SSHPassword))
		responseText += fmt.Sprintf("5. Максимум клиентов: `%d`\n", server.MaxClients)
		responseText += fmt.Sprintf("6. Статус: %s\n", getStatusEmoji(server.IsActive))

		// Создаем клавиатуру с кнопками для редактирования
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("1️⃣ IP", fmt.Sprintf("server_edit:ip:%d", server.ID)),
				tgbotapi.NewInlineKeyboardButtonData("2️⃣ Порт", fmt.Sprintf("server_edit:port:%d", server.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("3️⃣ SSH пользователь", fmt.Sprintf("server_edit:user:%d", server.ID)),
				tgbotapi.NewInlineKeyboardButtonData("4️⃣ SSH пароль", fmt.Sprintf("server_edit:pass:%d", server.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("5️⃣ Макс. клиентов", fmt.Sprintf("server_edit:max:%d", server.ID)),
				tgbotapi.NewInlineKeyboardButtonData("6️⃣ Статус", fmt.Sprintf("server_edit:status:%d", server.ID)),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к серверу", fmt.Sprintf("server_action:view:%d", server.ID)),
			),
		)

		msg := tgbotapi.NewMessage(chatID, responseText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return

	case "delete":
		// Получаем информацию о сервере для удаления
		server, err := h.db.GetServerByID(serverID)
		if err != nil {
			responseText = fmt.Sprintf("Ошибка: не удалось найти сервер #%d", serverID)
			break
		}

		// Проверяем, есть ли активные подписки на этом сервере
		var subscriptionsCount int
		err = h.db.DB.Get(&subscriptionsCount, "SELECT COUNT(*) FROM subscriptions WHERE server_id = $1 AND status = 'active'", serverID)
		if err != nil {
			log.Printf("Ошибка при проверке подписок сервера: %v", err)
			responseText = "Ошибка при проверке подписок сервера"
			break
		}

		if subscriptionsCount > 0 {
			responseText = fmt.Sprintf("❌ Невозможно удалить сервер #%d, так как на нем есть %d активных подписок.\n\nСначала переместите или отмените все подписки на этом сервере.", serverID, subscriptionsCount)

			// Добавляем кнопку для возврата
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к серверу", fmt.Sprintf("server_action:view:%d", server.ID)),
				),
			)

			msg := tgbotapi.NewMessage(chatID, responseText)
			msg.ReplyMarkup = keyboard
			h.bot.Send(msg)
			return
		}

		// Запрашиваем подтверждение удаления
		responseText = fmt.Sprintf("❓ Вы действительно хотите удалить сервер #%d (%s)?\n\nЭто действие нельзя отменить.", serverID, server.IP)

		// Добавляем кнопки подтверждения
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Да, удалить", fmt.Sprintf("server_confirm_delete:%d", server.ID)),
				tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", fmt.Sprintf("server_action:view:%d", server.ID)),
			),
		)

		msg := tgbotapi.NewMessage(chatID, responseText)
		msg.ReplyMarkup = keyboard
		h.bot.Send(msg)
		return

	default:
		responseText = fmt.Sprintf("Неизвестное действие '%s' для сервера #%d", action, serverID)
	}

	// Отправляем ответ пользователю
	msg := tgbotapi.NewMessage(chatID, responseText)
	h.bot.Send(msg)
}

// startServerAddition начинает процесс добавления нового сервера
func (h *BotHandler) startServerAddition(chatID int64) {
	// Сохраняем состояние пользователя
	h.userStates[chatID] = UserState{
		State: "add_server_ip",
		Data: map[string]string{
			"port":        "22",
			"max_clients": "10",
			"is_active":   "true",
		},
	}

	// Отправляем сообщение пользователю
	responseText := "🖥️ *Добавление нового сервера*\n\n"
	responseText += "Введите IP-адрес сервера:\n"
	responseText += "_(например, 123.45.67.89)_"

	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ParseMode = "Markdown"

	// Добавляем кнопку отмены
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", "admin_menu:servers"),
		),
	)
	msg.ReplyMarkup = keyboard

	h.bot.Send(msg)
}

// handleServerConfirmDelete обрабатывает подтверждение удаления сервера
func (h *BotHandler) handleServerConfirmDelete(chatID int64, serverID int) {
	// Получаем информацию о сервере
	server, err := h.db.GetServerByID(serverID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка: не удалось найти сервер #%d", serverID))
		h.bot.Send(msg)
		return
	}

	// Отправляем сообщение о начале удаления
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🗑️ Удаление сервера #%d (%s)...", serverID, server.IP))
	sentMsg, _ := h.bot.Send(msg)

	// Удаляем сервер из базы данных
	err = h.db.DeleteServer(serverID)
	if err != nil {
		editMsg := tgbotapi.NewEditMessageText(
			chatID,
			sentMsg.MessageID,
			fmt.Sprintf("❌ Ошибка при удалении сервера #%d: %v", serverID, err),
		)
		h.bot.Send(editMsg)
		return
	}

	// Добавляем кнопку возврата к списку серверов
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к списку серверов", "admin_menu:servers"),
		),
	)

	editMsgWithKeyboard := tgbotapi.NewEditMessageTextAndMarkup(
		chatID,
		sentMsg.MessageID,
		fmt.Sprintf("✅ Сервер #%d (%s) успешно удален", serverID, server.IP),
		keyboard,
	)

	h.bot.Send(editMsgWithKeyboard)
}

// maskPassword маскирует пароль, оставляя видимыми только первый и последний символы
func maskPassword(password string) string {
	if len(password) <= 2 {
		return "**"
	}

	return password[:1] + strings.Repeat("*", len(password)-2) + password[len(password)-1:]
}

// getStatusEmoji возвращает эмодзи для статуса
func getStatusEmoji(isActive bool) string {
	if isActive {
		return "🟢"
	}
	return "🔴"
}

// getStatusText преобразует булево значение активности в текст статуса
func getStatusText(isActive bool) string {
	if isActive {
		return "Активен"
	}
	return "Неактивен"
}

// handleSubscriptionAction обрабатывает действия с подписками
func (h *BotHandler) handleSubscriptionAction(chatID int64, action string, subscriptionID int) {
	// Получаем информацию о подписке
	subscription, err := h.db.GetSubscriptionByID(subscriptionID)
	if err != nil {
		log.Printf("Ошибка при получении информации о подписке #%d: %v", subscriptionID, err)
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка: не удалось найти подписку #%d", subscriptionID))
		h.bot.Send(msg)
		return
	}

	// Получаем информацию о пользователе
	user, err := h.db.GetUserByID(subscription.UserID)
	if err != nil {
		log.Printf("Ошибка при получении информации о пользователе #%d: %v", subscription.UserID, err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка: не удалось найти пользователя подписки")
		h.bot.Send(msg)
		return
	}

	// Получаем информацию о плане
	plan, err := h.db.GetSubscriptionPlanByID(subscription.PlanID)
	if err != nil {
		log.Printf("Ошибка при получении информации о плане #%d: %v", subscription.PlanID, err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка: не удалось найти план подписки")
		h.bot.Send(msg)
		return
	}

	// Получаем сервер
	server, err := h.db.GetServerByID(subscription.ServerID)
	if err != nil {
		log.Printf("Ошибка при получении информации о сервере #%d: %v", subscription.ServerID, err)
		msg := tgbotapi.NewMessage(chatID, "Ошибка: не удалось найти сервер подписки")
		h.bot.Send(msg)
		return
	}

	// Отправляем сообщение о том, что начали обработку
	processingMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⏳ Выполняется операция с подпиской #%d пользователя %s...",
		subscriptionID, user.Username))
	sentMsg, _ := h.bot.Send(processingMsg)

	var responseText string

	switch action {
	case "block":
		// Проверяем, заблокирована ли уже подписка (с таймаутом)
		log.Printf("Отправка команды блокировки для подписки #%d", subscriptionID)

		// Создаем канал для обработки таймаута
		done := make(chan bool, 1)
		var blockErr error

		// Запускаем операцию в отдельной горутине
		go func() {
			err := h.vpnManager.BlockClient(server, subscription.ConfigFilePath)
			if err != nil {
				blockErr = err
			}
			done <- true
		}()

		// Устанавливаем таймаут 10 секунд
		select {
		case <-done:
			if blockErr != nil {
				log.Printf("Ошибка при блокировке подписки #%d: %v", subscriptionID, blockErr)
				responseText = fmt.Sprintf("❌ Ошибка при блокировке подписки #%d: не удалось подключиться к серверу VPN.\n\nВозможно, сервер временно недоступен. Пожалуйста, повторите попытку позже.", subscriptionID)
			} else {
				log.Printf("Подписка #%d успешно заблокирована", subscriptionID)
				responseText = fmt.Sprintf("✅ Подписка #%d пользователя %s успешно заблокирована", subscriptionID, user.Username)

				// Отправляем уведомление пользователю о блокировке
				userMsg := fmt.Sprintf("❗ Ваша подписка #%d (%s) была заблокирована администратором", subscriptionID, plan.Name)
				notificationMsg := tgbotapi.NewMessage(user.TelegramID, userMsg)
				h.bot.Send(notificationMsg)
			}
		case <-time.After(10 * time.Second):
			log.Printf("Таймаут при блокировке подписки #%d", subscriptionID)
			responseText = fmt.Sprintf("⚠️ Превышено время ожидания при попытке заблокировать подписку #%d.\n\nСервер VPN не отвечает. Попробуйте повторить операцию позже.", subscriptionID)
		}

	case "unblock":
		log.Printf("Отправка команды разблокировки для подписки #%d", subscriptionID)

		// Создаем канал для обработки таймаута
		done := make(chan bool, 1)
		var unblockErr error

		// Запускаем операцию в отдельной горутине
		go func() {
			err := h.vpnManager.UnblockClient(server, subscription.ConfigFilePath)
			if err != nil {
				unblockErr = err
			}
			done <- true
		}()

		// Устанавливаем таймаут 10 секунд
		select {
		case <-done:
			if unblockErr != nil {
				log.Printf("Ошибка при разблокировке подписки #%d: %v", subscriptionID, unblockErr)
				responseText = fmt.Sprintf("❌ Ошибка при разблокировке подписки #%d: не удалось подключиться к серверу VPN.\n\nВозможно, сервер временно недоступен. Пожалуйста, повторите попытку позже.", subscriptionID)
			} else {
				log.Printf("Подписка #%d успешно разблокирована", subscriptionID)
				responseText = fmt.Sprintf("✅ Подписка #%d пользователя %s успешно разблокирована", subscriptionID, user.Username)

				// Отправляем уведомление пользователю о разблокировке
				userMsg := fmt.Sprintf("✅ Ваша подписка #%d (%s) была разблокирована администратором", subscriptionID, plan.Name)
				notificationMsg := tgbotapi.NewMessage(user.TelegramID, userMsg)
				h.bot.Send(notificationMsg)
			}
		case <-time.After(10 * time.Second):
			log.Printf("Таймаут при разблокировке подписки #%d", subscriptionID)
			responseText = fmt.Sprintf("⚠️ Превышено время ожидания при попытке разблокировать подписку #%d.\n\nСервер VPN не отвечает. Попробуйте повторить операцию позже.", subscriptionID)
		}

	case "delete":
		log.Printf("Отзыв конфигурации для клиента %s (файл: %s)",
			subscription.ConfigFilePath, subscription.ConfigFilePath)

		// Создаем канал для обработки таймаута
		done := make(chan bool, 1)
		var revokeErr error

		// Запускаем операцию в отдельной горутине
		go func() {
			err := h.vpnManager.RevokeClientConfig(server, subscription.ConfigFilePath)
			if err != nil {
				revokeErr = err
			}
			done <- true
		}()

		// Устанавливаем таймаут 10 секунд
		select {
		case <-done:
			if revokeErr != nil {
				log.Printf("Ошибка при отзыве конфигурации VPN для подписки #%d: %v", subscriptionID, revokeErr)
				// Всё равно меняем статус подписки на отозванный
				subscription.Status = "revoked"
				err = h.db.UpdateSubscription(subscription)
				if err != nil {
					log.Printf("Ошибка при обновлении статуса подписки #%d: %v", subscriptionID, err)
					responseText = fmt.Sprintf("❌ Ошибка при обновлении статуса подписки. Сервер VPN недоступен.")
				} else {
					responseText = fmt.Sprintf("⚠️ Подписка #%d пользователя %s помечена как отозванная, но сервер VPN недоступен. Конфигурация клиента будет отозвана автоматически, когда сервер станет доступен.", subscriptionID, user.Username)
				}
			} else {
				// Обновляем статус подписки на отозванный
				subscription.Status = "revoked"
				err = h.db.UpdateSubscription(subscription)
				if err != nil {
					log.Printf("Ошибка при обновлении статуса подписки #%d: %v", subscriptionID, err)
					responseText = fmt.Sprintf("❌ Подписка отозвана на сервере, но произошла ошибка при обновлении статуса в базе данных")
				} else {
					responseText = fmt.Sprintf("✅ Подписка #%d пользователя %s успешно отозвана", subscriptionID, user.Username)

					// Отправляем уведомление пользователю
					userMsg := fmt.Sprintf("❗ Ваша подписка #%d (%s) была отозвана администратором", subscriptionID, plan.Name)
					notificationMsg := tgbotapi.NewMessage(user.TelegramID, userMsg)
					h.bot.Send(notificationMsg)
				}
			}
		case <-time.After(10 * time.Second):
			log.Printf("Таймаут при отзыве подписки #%d", subscriptionID)
			// Всё равно меняем статус подписки на отозванный
			subscription.Status = "revoked"
			err = h.db.UpdateSubscription(subscription)
			if err != nil {
				log.Printf("Ошибка при обновлении статуса подписки #%d: %v", subscriptionID, err)
				responseText = fmt.Sprintf("❌ Ошибка при обновлении статуса подписки. Сервер VPN не отвечает.")
			} else {
				responseText = fmt.Sprintf("⚠️ Превышено время ожидания при отзыве подписки #%d, но она помечена как отозванная в базе данных. Конфигурация клиента будет отозвана автоматически, когда сервер станет доступен.", subscriptionID)
			}
		}

	default:
		responseText = fmt.Sprintf("Неизвестное действие '%s' для подписки #%d", action, subscriptionID)
	}

	// Отправляем ответ администратору (редактируем предыдущее сообщение)
	editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, responseText)
	h.bot.Send(editMsg)
}

// handleUserAction обрабатывает действия с пользователями
func (h *BotHandler) handleUserAction(chatID int64, action string, userID int) {
	log.Printf("Вызов handleUserAction: chatID=%d, action=%s, userID=%d", chatID, action, userID)

	// Получаем информацию о пользователе
	user, err := h.db.GetUserByID(userID)
	if err != nil {
		log.Printf("Ошибка при получении информации о пользователе #%d: %v", userID, err)
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка: не удалось найти пользователя #%d", userID))
		h.bot.Send(msg)
		return
	}

	log.Printf("Пользователь найден: ID=%d, username=%s", user.ID, user.Username)

	switch action {
	case "subscriptions":
		log.Printf("Обработка запроса на просмотр подписок для пользователя #%d", userID)
		// Получаем подписки пользователя
		subscriptions, err := h.db.GetSubscriptionsByUserID(userID)
		if err != nil {
			log.Printf("Ошибка при получении подписок пользователя #%d: %v", userID, err)
			msg := tgbotapi.NewMessage(chatID, "Ошибка при получении подписок пользователя")
			h.bot.Send(msg)
			return
		}

		log.Printf("Получено подписок: %d для пользователя #%d", len(subscriptions), userID)

		if len(subscriptions) == 0 {
			log.Printf("У пользователя #%d нет подписок", userID)
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("У пользователя %s нет подписок", user.Username))
			h.bot.Send(msg)
			return
		}

		// Формируем сообщение с подписками
		messageText := fmt.Sprintf("📋 Подписки пользователя %s:\n\n", user.Username)

		for i, subscription := range subscriptions {
			// Получаем план подписки
			plan, err := h.db.GetSubscriptionPlanByID(subscription.PlanID)
			if err != nil {
				log.Printf("Ошибка при получении плана #%d: %v", subscription.PlanID, err)
				continue
			}

			// Определяем статус подписки
			var statusEmoji string
			switch subscription.Status {
			case "active":
				statusEmoji = "✅"
			case "expired":
				statusEmoji = "⏱"
			case "revoked":
				statusEmoji = "❌"
			default:
				statusEmoji = "❓"
			}

			// Не проверяем статус блокировки - это долгая операция, которая может таймаутиться
			// Просто добавляем заметку, что статус может быть неточным
			blockedStatus := ""
			if subscription.Status == "active" {
				blockedStatus = " [статус блокировки: неизвестен]"
			}

			// Форматируем дату
			endDateStr := subscription.EndDate.Format("02.01.2006")

			// Добавляем информацию о подписке
			messageText += fmt.Sprintf("%d. #%d - %s %s%s\n   План: %s\n   Дата окончания: %s\n\n",
				i+1, subscription.ID, statusEmoji, subscription.Status, blockedStatus, plan.Name, endDateStr)
		}

		// Создаем клавиатуру с действиями для подписок
		var keyboardButtons [][]tgbotapi.InlineKeyboardButton

		for _, subscription := range subscriptions {
			// Если подписка активна, добавляем кнопки для блокировки/разблокировки и удаления
			if subscription.Status == "active" {
				// Не проверяем статус блокировки - предлагаем обе кнопки
				blockButton := tgbotapi.NewInlineKeyboardButtonData(
					fmt.Sprintf("🔒 Заблокировать #%d", subscription.ID),
					fmt.Sprintf("subscription_action:block:%d", subscription.ID),
				)
				unblockButton := tgbotapi.NewInlineKeyboardButtonData(
					fmt.Sprintf("🔓 Разблокировать #%d", subscription.ID),
					fmt.Sprintf("subscription_action:unblock:%d", subscription.ID),
				)

				// Добавляем обе кнопки для блокировки и разблокировки
				keyboardButtons = append(keyboardButtons, []tgbotapi.InlineKeyboardButton{blockButton})
				keyboardButtons = append(keyboardButtons, []tgbotapi.InlineKeyboardButton{unblockButton})

				// Добавляем кнопку удаления
				deleteButton := tgbotapi.NewInlineKeyboardButtonData(
					fmt.Sprintf("❌ Удалить #%d", subscription.ID),
					fmt.Sprintf("subscription_action:delete:%d", subscription.ID),
				)
				keyboardButtons = append(keyboardButtons, []tgbotapi.InlineKeyboardButton{deleteButton})
			}
		}

		// Добавляем кнопку "Назад"
		backButton := tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "admin_menu:users")
		keyboardButtons = append(keyboardButtons, []tgbotapi.InlineKeyboardButton{backButton})

		// Создаем клавиатуру
		keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardButtons...)

		// Отправляем сообщение с подписками и клавиатурой
		msg := tgbotapi.NewMessage(chatID, messageText)
		msg.ReplyMarkup = keyboard

		log.Printf("Отправка сообщения с подписками для пользователя #%d. Длина сообщения: %d символов", userID, len(messageText))
		sentMsg, err := h.bot.Send(msg)
		if err != nil {
			log.Printf("Ошибка при отправке сообщения с подписками: %v", err)
		} else {
			log.Printf("Сообщение с подписками успешно отправлено, message_id=%d", sentMsg.MessageID)
		}

	case "make_admin":
		// Назначаем пользователя администратором
		err = h.db.SetUserAdmin(userID, true)
		if err != nil {
			log.Printf("Ошибка при назначении пользователя #%d администратором: %v", userID, err)
			msg := tgbotapi.NewMessage(chatID, "Ошибка при назначении пользователя администратором")
			h.bot.Send(msg)
			return
		}

		// Отправляем уведомление пользователю
		userMsg := "✅ Вам были предоставлены права администратора в боте. Теперь вы имеете доступ к дополнительным функциям. Используйте команду /admin для доступа к панели администратора."
		notificationMsg := tgbotapi.NewMessage(user.TelegramID, userMsg)
		h.bot.Send(notificationMsg)

		// Отправляем ответ администратору
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Пользователь %s успешно назначен администратором", user.Username))
		h.bot.Send(msg)

	case "remove_admin":
		// Снимаем права администратора
		err = h.db.SetUserAdmin(userID, false)
		if err != nil {
			log.Printf("Ошибка при снятии прав администратора у пользователя #%d: %v", userID, err)
			msg := tgbotapi.NewMessage(chatID, "Ошибка при снятии прав администратора")
			h.bot.Send(msg)
			return
		}

		// Отправляем уведомление пользователю
		userMsg := "❗ Ваши права администратора в боте были отозваны."
		notificationMsg := tgbotapi.NewMessage(user.TelegramID, userMsg)
		h.bot.Send(notificationMsg)

		// Отправляем ответ администратору
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Права администратора успешно сняты с пользователя %s", user.Username))
		h.bot.Send(msg)

	default:
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Неизвестное действие для пользователя #%d", userID))
		h.bot.Send(msg)
	}
}

// checkServerAvailability проверяет доступность сервера и отправляет результат пользователю
func (h *BotHandler) checkServerAvailability(chatID int64, serverID int) {
	// Отправляем сообщение о начале проверки
	msg := tgbotapi.NewMessage(chatID, "🔄 Проверка доступности сервера...")
	sentMsg, _ := h.bot.Send(msg)

	// Создаем обновляемое сообщение
	msgText := "🔍 Проверка доступности сервера:\n\n"

	// Получаем информацию о сервере из БД
	server, err := h.db.GetServerByID(serverID)
	if err != nil {
		msgText += "❌ Ошибка: сервер не найден в базе данных"
		editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
		h.bot.Send(editMsg)
		return
	}

	msgText += fmt.Sprintf("🖥️ Сервер: %s (ID: %d)\n", server.IP, server.ID)
	editMsg := tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	// Проверяем TCP-соединение
	msgText += "🔄 Проверка TCP-соединения...\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	timeout := 5 * time.Second
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", server.IP, server.Port), timeout)
	if err != nil {
		msgText += fmt.Sprintf("❌ TCP-соединение: Ошибка - %v\n", err)
		editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
		h.bot.Send(editMsg)

		// Добавляем кнопку для возврата к списку серверов
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к списку серверов", "admin_menu:servers"),
			),
		)
		editMsgWithKeyboard := tgbotapi.NewEditMessageTextAndMarkup(
			chatID,
			sentMsg.MessageID,
			msgText,
			keyboard,
		)
		h.bot.Send(editMsgWithKeyboard)
		return
	}

	conn.Close()
	msgText += "✅ TCP-соединение: Установлено\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	// Проверяем SSH-соединение
	msgText += "🔄 Проверка SSH-соединения...\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	// Создаем клиента SSH
	sshConfig := &ssh.ClientConfig{
		User: server.SSHUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.SSHPassword),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// Подключаемся по SSH
	addr := fmt.Sprintf("%s:%d", server.IP, server.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		msgText += fmt.Sprintf("❌ SSH-соединение: Ошибка - %v\n", err)
		editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
		h.bot.Send(editMsg)

		// Добавляем кнопку для возврата к списку серверов
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к списку серверов", "admin_menu:servers"),
			),
		)
		editMsgWithKeyboard := tgbotapi.NewEditMessageTextAndMarkup(
			chatID,
			sentMsg.MessageID,
			msgText,
			keyboard,
		)
		h.bot.Send(editMsgWithKeyboard)
		return
	}

	defer sshClient.Close()
	msgText += "✅ SSH-соединение: Установлено\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	// Проверяем наличие Wireguard
	msgText += "🔄 Проверка Wireguard...\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	session, err := sshClient.NewSession()
	if err != nil {
		msgText += fmt.Sprintf("❌ Создание SSH-сессии: Ошибка - %v\n", err)
	} else {
		defer session.Close()

		var stdout bytes.Buffer
		session.Stdout = &stdout

		if err := session.Run("which wg"); err != nil {
			msgText += "❌ Wireguard: Не установлен\n"
		} else {
			msgText += "✅ Wireguard: Установлен\n"
		}
	}

	// Проверяем конфигурацию Wireguard
	msgText += "🔄 Проверка конфигурации Wireguard...\n"
	editMsg = tgbotapi.NewEditMessageText(chatID, sentMsg.MessageID, msgText)
	h.bot.Send(editMsg)

	// Создаем новую сессию
	session, err = sshClient.NewSession()
	if err != nil {
		msgText += fmt.Sprintf("❌ Создание SSH-сессии: Ошибка - %v\n", err)
	} else {
		defer session.Close()

		var stdout bytes.Buffer
		session.Stdout = &stdout

		if err := session.Run("sudo cat /etc/wireguard/wg0.conf 2>/dev/null | grep -c '\\[Interface\\]' || echo '0'"); err != nil {
			msgText += "❌ Конфигурация Wireguard: Не найдена\n"
		} else {
			count := strings.TrimSpace(stdout.String())
			if count != "0" {
				msgText += "✅ Конфигурация Wireguard: Найдена\n"

				// Проверяем количество клиентов
				session, err = sshClient.NewSession()
				if err == nil {
					defer session.Close()
					stdout.Reset()
					session.Stdout = &stdout
					if err := session.Run("sudo cat /etc/wireguard/wg0.conf 2>/dev/null | grep -c '\\[Peer\\]' || echo '0'"); err == nil {
						peerCount := strings.TrimSpace(stdout.String())

						// Обновляем количество клиентов в базе данных
						peerCountInt, _ := strconv.Atoi(peerCount)
						if server.CurrentClients != peerCountInt {
							server.CurrentClients = peerCountInt
							err := h.db.UpdateServer(server)
							if err != nil {
								log.Printf("Ошибка при обновлении счетчика клиентов сервера: %v", err)
							} else {
								log.Printf("Обновлено количество клиентов для сервера %d: %d", server.ID, peerCountInt)
							}
						}

						msgText += fmt.Sprintf("👥 Активных клиентов: %s\n", peerCount)
					}
				}
			} else {
				msgText += "❌ Конфигурация Wireguard: Не найдена\n"
			}
		}
	}

	// Добавляем статус успешной проверки и время
	msgText += fmt.Sprintf("\n✅ Проверка завершена успешно!\n⏱️ Время: %s", time.Now().Format("02.01.2006 15:04:05"))

	// Добавляем кнопки для действий с сервером
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Повторить проверку", fmt.Sprintf("server_action:check:%d", server.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📝 Редактировать", fmt.Sprintf("server_action:edit:%d", server.ID)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Удалить", fmt.Sprintf("server_action:delete:%d", server.ID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("◀️ Назад к списку серверов", "admin_menu:servers"),
		),
	)

	editMsgWithKeyboard := tgbotapi.NewEditMessageTextAndMarkup(
		chatID,
		sentMsg.MessageID,
		msgText,
		keyboard,
	)
	h.bot.Send(editMsgWithKeyboard)
}
