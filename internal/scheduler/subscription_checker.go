package scheduler

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/ilokitv/botVPN/internal/database"
	"github.com/ilokitv/botVPN/internal/models"
	"github.com/ilokitv/botVPN/internal/vpn"
)

// SubscriptionChecker - структура для проверки истекших подписок
type SubscriptionChecker struct {
	db         *database.DB
	vpnManager *vpn.WireguardManager
	bot        *tgbotapi.BotAPI
	interval   time.Duration // Интервал между проверками
	stop       chan struct{} // Канал для остановки проверок
}

// NewSubscriptionChecker создает новый объект для проверки подписок
func NewSubscriptionChecker(db *database.DB, vpnManager *vpn.WireguardManager, bot *tgbotapi.BotAPI, interval time.Duration) *SubscriptionChecker {
	return &SubscriptionChecker{
		db:         db,
		vpnManager: vpnManager,
		bot:        bot,
		interval:   interval,
		stop:       make(chan struct{}),
	}
}

// Start запускает фоновую задачу для проверки подписок
func (sc *SubscriptionChecker) Start() {
	log.Println("Запуск фоновой задачи проверки подписок")

	// Сразу запускаем первую проверку
	go sc.checkExpiredSubscriptions()

	// Настраиваем периодическую проверку
	ticker := time.NewTicker(sc.interval)
	go func() {
		for {
			select {
			case <-ticker.C:
				go sc.checkExpiredSubscriptions()
			case <-sc.stop:
				ticker.Stop()
				return
			}
		}
	}()
}

// Stop останавливает проверку подписок
func (sc *SubscriptionChecker) Stop() {
	log.Println("Остановка фоновой задачи проверки подписок")
	close(sc.stop)
}

// checkExpiredSubscriptions проверяет все активные подписки и обрабатывает истекшие
func (sc *SubscriptionChecker) checkExpiredSubscriptions() {
	log.Println("Проверка истекших подписок...")

	// Получаем все активные подписки
	subscriptions, err := sc.getActiveSubscriptions()
	if err != nil {
		log.Printf("Ошибка при получении активных подписок: %v", err)
		return
	}

	log.Printf("Найдено %d активных подписок для проверки", len(subscriptions))

	now := time.Now()
	expiredCount := 0
	var expiredSubscriptions []models.Subscription

	for _, subscription := range subscriptions {
		// Проверяем, истекла ли подписка
		if now.After(subscription.EndDate) {
			log.Printf("Обнаружена истекшая подписка #%d, пользователь #%d, дата окончания: %s",
				subscription.ID, subscription.UserID, subscription.EndDate.Format("02.01.2006"))

			// Обновляем статус подписки на "expired"
			err = sc.expireSubscription(&subscription)
			if err != nil {
				log.Printf("Ошибка при обновлении статуса подписки #%d: %v", subscription.ID, err)
				continue
			}

			// Отзываем конфигурацию VPN
			err = sc.revokeVPNConfig(&subscription)
			if err != nil {
				log.Printf("Ошибка при отзыве конфигурации VPN для подписки #%d: %v", subscription.ID, err)
				continue
			}

			// Отправляем уведомление пользователю
			err = sc.notifyUser(&subscription)
			if err != nil {
				log.Printf("Ошибка при отправке уведомления пользователю #%d: %v", subscription.UserID, err)
			}

			log.Printf("Подписка #%d успешно помечена как истекшая и VPN-конфигурация отозвана", subscription.ID)

			expiredCount++
			expiredSubscriptions = append(expiredSubscriptions, subscription)
		} else {
			// Проверяем, скоро ли истечет подписка (осталось менее 3 дней)
			daysLeft := int(subscription.EndDate.Sub(now).Hours() / 24)
			if daysLeft <= 3 && daysLeft >= 0 {
				// Отправляем предупреждение о скором истечении
				err = sc.notifyUserAboutExpiration(&subscription, daysLeft)
				if err != nil {
					log.Printf("Ошибка при отправке предупреждения о скором истечении пользователю #%d: %v", subscription.UserID, err)
				}
			}
		}
	}

	// Если были найдены истекшие подписки, отправляем отчет администраторам
	if expiredCount > 0 {
		err = sc.notifyAdmins(expiredSubscriptions)
		if err != nil {
			log.Printf("Ошибка при отправке отчета администраторам: %v", err)
		}
	}

	log.Println("Проверка истекших подписок завершена")
}

// getActiveSubscriptions получает все активные подписки
func (sc *SubscriptionChecker) getActiveSubscriptions() ([]models.Subscription, error) {
	// Получаем все подписки со статусом "active"
	query := "SELECT * FROM subscriptions WHERE status = 'active'"
	var subscriptions []models.Subscription
	err := sc.db.Select(&subscriptions, query)
	if err != nil {
		return nil, err
	}
	return subscriptions, nil
}

// expireSubscription устанавливает статус подписки как "expired"
func (sc *SubscriptionChecker) expireSubscription(subscription *models.Subscription) error {
	subscription.Status = "expired"
	return sc.db.UpdateSubscription(subscription)
}

// revokeVPNConfig отзывает конфигурацию VPN с сервера
func (sc *SubscriptionChecker) revokeVPNConfig(subscription *models.Subscription) error {
	// Получаем информацию о сервере
	server, err := sc.db.GetServerByID(subscription.ServerID)
	if err != nil {
		return err
	}

	// Отзываем конфигурацию клиента
	return sc.vpnManager.RevokeClientConfig(server, subscription.ConfigFilePath)
}

// notifyUser отправляет уведомление пользователю об истечении подписки
func (sc *SubscriptionChecker) notifyUser(subscription *models.Subscription) error {
	// Получаем информацию о пользователе
	user, err := sc.db.GetUserByID(subscription.UserID)
	if err != nil {
		return err
	}

	// Получаем информацию о плане подписки
	plan, err := sc.db.GetSubscriptionPlanByID(subscription.PlanID)
	if err != nil {
		return err
	}

	// Формируем сообщение об истечении подписки
	message := fmt.Sprintf(
		"❗️ *Срок действия вашей подписки истек* ❗️\n\n"+
			"Подписка: #%d\n"+
			"План: %s\n"+
			"Дата начала: %s\n"+
			"Дата окончания: %s\n\n"+
			"Ваше VPN-соединение было автоматически отключено.\n"+
			"Для продолжения использования VPN, пожалуйста, оформите новую подписку с помощью команды /buy.",
		subscription.ID,
		plan.Name,
		subscription.StartDate.Format("02.01.2006"),
		subscription.EndDate.Format("02.01.2006"),
	)

	// Отправляем сообщение пользователю
	msg := tgbotapi.NewMessage(user.TelegramID, message)
	msg.ParseMode = "Markdown"

	_, err = sc.bot.Send(msg)
	return err
}

// notifyUserAboutExpiration отправляет предупреждение пользователю о скором истечении подписки
func (sc *SubscriptionChecker) notifyUserAboutExpiration(subscription *models.Subscription, daysLeft int) error {
	// Получаем информацию о пользователе
	user, err := sc.db.GetUserByID(subscription.UserID)
	if err != nil {
		return err
	}

	// Получаем информацию о плане подписки
	plan, err := sc.db.GetSubscriptionPlanByID(subscription.PlanID)
	if err != nil {
		return err
	}

	// Формируем сообщение о скором истечении подписки
	message := fmt.Sprintf(
		"⚠️ *Внимание! Ваша подписка скоро истечет* ⚠️\n\n"+
			"Подписка: #%d\n"+
			"План: %s\n"+
			"Дата окончания: %s\n\n"+
			"Осталось дней: *%d*\n\n"+
			"Для продления подписки используйте команду /buy.\n"+
			"Если не продлить подписку, ваше VPN-соединение будет автоматически отключено по истечении срока.",
		subscription.ID,
		plan.Name,
		subscription.EndDate.Format("02.01.2006"),
		daysLeft,
	)

	// Отправляем сообщение пользователю
	msg := tgbotapi.NewMessage(user.TelegramID, message)
	msg.ParseMode = "Markdown"

	_, err = sc.bot.Send(msg)
	return err
}

// notifyAdmins отправляет отчет администраторам о обработанных истекших подписках
func (sc *SubscriptionChecker) notifyAdmins(expiredSubscriptions []models.Subscription) error {
	// Получаем список администраторов
	admins, err := sc.db.GetAllAdmins()
	if err != nil {
		return fmt.Errorf("не удалось получить список администраторов: %w", err)
	}

	if len(admins) == 0 {
		log.Println("Нет администраторов для отправки отчета")
		return nil
	}

	// Формируем сообщение с отчетом
	message := fmt.Sprintf(
		"📊 *Отчет о истекших подписках*\n\n"+
			"Обнаружено и обработано истекших подписок: %d\n\n"+
			"*Список обработанных подписок:*\n",
		len(expiredSubscriptions),
	)

	// Добавляем информацию о каждой подписке
	for i, subscription := range expiredSubscriptions {
		// Получаем информацию о пользователе
		user, err := sc.db.GetUserByID(subscription.UserID)
		if err != nil {
			log.Printf("Ошибка при получении информации о пользователе #%d: %v", subscription.UserID, err)
			continue
		}

		// Получаем информацию о плане
		plan, err := sc.db.GetSubscriptionPlanByID(subscription.PlanID)
		if err != nil {
			log.Printf("Ошибка при получении информации о плане #%d: %v", subscription.PlanID, err)
			continue
		}

		userInfo := fmt.Sprintf("%s", user.Username)
		if userInfo == "" {
			userInfo = fmt.Sprintf("ID: %d", user.TelegramID)
		}

		message += fmt.Sprintf(
			"%d. Подписка #%d - Пользователь: %s - План: %s - Дата окончания: %s\n",
			i+1,
			subscription.ID,
			userInfo,
			plan.Name,
			subscription.EndDate.Format("02.01.2006"),
		)
	}

	message += "\nВсе указанные подписки были автоматически помечены как истекшие, и соответствующие VPN-конфигурации были отозваны."

	// Отправляем сообщение каждому администратору
	for _, admin := range admins {
		msg := tgbotapi.NewMessage(admin.TelegramID, message)
		msg.ParseMode = "Markdown"

		_, err := sc.bot.Send(msg)
		if err != nil {
			log.Printf("Ошибка при отправке отчета администратору #%d: %v", admin.TelegramID, err)
		}
	}

	return nil
}
