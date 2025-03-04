-- Отключаем проверку внешних ключей
SET session_replication_role = 'replica';

-- Очищаем таблицы в правильном порядке
TRUNCATE payments CASCADE;
TRUNCATE subscriptions CASCADE;
TRUNCATE users CASCADE;
TRUNCATE servers CASCADE;

-- Включаем проверку внешних ключей
SET session_replication_role = 'origin';

-- Проверяем количество записей в каждой таблице после очистки
SELECT 'payments' as table_name, COUNT(*) as count FROM payments
UNION ALL
SELECT 'subscriptions', COUNT(*) FROM subscriptions
UNION ALL
SELECT 'users', COUNT(*) FROM users
UNION ALL
SELECT 'servers', COUNT(*) FROM servers
UNION ALL
SELECT 'subscription_plans', COUNT(*) FROM subscription_plans; 