package rclone

import (
	"nascloud/internal/provider"

	_ "github.com/rclone/rclone/backend/b2"
	_ "github.com/rclone/rclone/backend/box"
	_ "github.com/rclone/rclone/backend/drive"
	_ "github.com/rclone/rclone/backend/dropbox"
	_ "github.com/rclone/rclone/backend/local" // тесты драйвера
	_ "github.com/rclone/rclone/backend/onedrive"
	_ "github.com/rclone/rclone/backend/pcloud"
	_ "github.com/rclone/rclone/backend/s3"
)

// Descriptors возвращает облака rclone, подключённые в пакет.
func Descriptors() []provider.Descriptor {
	return []provider.Descriptor{
		s3Descriptor(),
		b2Descriptor(),
		oauthDescriptor("drive", "Google Drive", nil),
		oauthDescriptor("dropbox", "Dropbox", nil),
		oauthDescriptor("onedrive", "Microsoft OneDrive", []provider.Field{
			{Key: "region", Label: "Регион", Default: "global", Options: []provider.Option{{Value: "global", Label: "Глобальный"}, {Value: "us", Label: "США"}, {Value: "de", Label: "Германия"}, {Value: "cn", Label: "Китай (21Vianet)"}}},
		}),
		oauthDescriptor("box", "Box", nil),
		oauthDescriptor("pcloud", "pCloud", []provider.Field{
			{Key: "hostname", Label: "Сервер", Default: "api.pcloud.com", Options: []provider.Option{{Value: "api.pcloud.com", Label: "США"}, {Value: "eapi.pcloud.com", Label: "Европа"}}},
		}),
	}
}

// backend добавляет общие поля (путь в облаке и поля пакета) к полям облака.
func backend(kind, title, help string, fields []provider.Field, steps []provider.Step) provider.Descriptor {
	all := append([]provider.Field{{Key: "root", Label: "Бакет и папка", Placeholder: "мой-бакет/backups", Help: "Путь внутри облака. Для S3 и B2 первая часть — имя бакета."}}, fields...)
	all = append(all, provider.CommonFields()...)
	return provider.Descriptor{
		Kind: kind, Title: title, Fields: all, Help: help, Steps: steps,
		New: func(s map[string]string) (provider.Provider, error) { return New(kind, s) },
	}
}

func s3Descriptor() provider.Descriptor {
	return backend("s3", "Amazon S3 и совместимые",
		"Ключи доступа создаются в консоли сервиса. Для S3-совместимых сервисов укажите адрес (endpoint).",
		[]provider.Field{
			{Key: "provider", Label: "Сервис", Default: "AWS", Options: []provider.Option{{Value: "AWS", Label: "Amazon S3"}, {Value: "Other", Label: "Другой S3-совместимый"}}},
			{Key: "access_key_id", Label: "Access Key ID", Secret: true},
			{Key: "secret_access_key", Label: "Secret Access Key", Secret: true},
			{Key: "region", Label: "Регион", Placeholder: "us-east-1"},
			{Key: "endpoint", Label: "Адрес сервиса (endpoint)", Placeholder: "пусто для Amazon S3", Help: "Нужен только для S3-совместимых сервисов, например https://storage.example.com."},
		},
		[]provider.Step{
			{Title: "Создайте ключ доступа", Text: "В консоли облачного сервиса создайте пару Access Key ID и Secret Access Key."},
			{Title: "Создайте бакет", Text: "Бакет — контейнер для файлов. Укажите его в поле «Бакет и папка»."},
			{Title: "Введите данные", Text: "Заполните поля и нажмите «Проверить подключение»."},
		})
}

func b2Descriptor() provider.Descriptor {
	return backend("b2", "Backblaze B2",
		"Ключ приложения создаётся в личном кабинете Backblaze: Application Keys.",
		[]provider.Field{
			{Key: "account", Label: "Key ID", Help: "Идентификатор ключа приложения (keyID).", Secret: true},
			{Key: "key", Label: "Application Key", Help: "Секретный ключ приложения. Показывается один раз при создании.", Secret: true},
		},
		[]provider.Step{
			{Title: "Создайте ключ приложения", Text: "В кабинете Backblaze откройте Application Keys и создайте ключ с доступом к нужному бакету."},
			{Title: "Создайте бакет", Text: "Бакет укажите в поле «Бакет и папка»."},
			{Title: "Введите данные", Text: "Вставьте keyID и ключ, затем нажмите «Проверить подключение»."},
		})
}

// oauthDescriptor описывает облако с входом через OAuth. Токен получает пользователь
// командой rclone authorize на компьютере с браузером: NAS не открывает страницу входа.
func oauthDescriptor(kind, title string, extra []provider.Field) provider.Descriptor {
	fields := append([]provider.Field{
		{Key: "token", Label: "Токен", Secret: true, Help: "JSON, который выводит команда rclone authorize. Вставьте его целиком."},
		{Key: "client_id", Label: "ID приложения", Help: "Необязательно. Пусто — встроенное приложение rclone, у него общий лимит запросов."},
		{Key: "client_secret", Label: "Секрет приложения", Secret: true, Help: "Необязательно, нужен вместе с ID приложения."},
	}, extra...)
	return backend(kind, title,
		"Токен получают на компьютере с браузером и вставляют в поле «Токен».",
		fields,
		[]provider.Step{
			{Title: "Установите rclone", Text: "Скачайте rclone с https://rclone.org/downloads/ на компьютер, где есть браузер."},
			{Title: "Получите токен", Text: "Выполните команду: rclone authorize \"" + kind + "\". Войдите в аккаунт в открывшемся окне."},
			{Title: "Вставьте токен", Text: "Скопируйте весь JSON из терминала в поле «Токен» и нажмите «Проверить подключение»."},
		})
}
