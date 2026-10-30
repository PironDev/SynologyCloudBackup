// Package mailru — вендорская обёртка над WebDAV для Облака Mail.ru.
// Публичного REST API у Облака для обычных аккаунтов нет; Mail.ru официально
// поддерживает WebDAV (https://help.mail.ru/cloud/desktop/webdav/).
package mailru

import (
	"errors"
	"strings"

	"nascloud/internal/provider"
	"nascloud/internal/provider/mailru/cloud"
	"nascloud/internal/provider/webdav"
)

// DefaultBase — WebDAV-адрес Облака Mail.ru.
const DefaultBase = "https://webdav.cloud.mail.ru"

// Descriptor описывает вендорскую сборку для Облака Mail.ru.
func Descriptor() provider.Descriptor {
	d := provider.Descriptor{
		Kind:  "mailru",
		Title: "Облако Mail.ru",
		Fields: []provider.Field{
			{Key: "login", Label: "Почта (логин)", Placeholder: "name@mail.ru",
				Help: "Адрес ящика, к которому подключено Облако"},
			{Key: "password", Label: "Пароль внешнего приложения", Secret: true,
				Help: "Не основной пароль от почты, а пароль для внешнего приложения"},
			{Key: "transport", Label: "Тип подключения", Default: "webdav",
				Options: []provider.Option{{Value: "webdav", Label: "WebDAV (официальный, рекомендуется)"}, {Value: "api", Label: "Приватный API cloud.mail.ru (неофициальный)"}},
				Help:    "WebDAV — официальный протокол Облака, пароль внешнего приложения. Приватный API — протокол веб-клиента с хешем mrhash и загрузкой через шард; Mail.ru может изменить его без предупреждения. Переключайтесь, если WebDAV недоступен. Для каждого аккаунта можно выбрать свой тип подключения."},
			{Key: "speedup_file_patterns", Label: "Загрузка по хешу: шаблоны имён", Default: cloud.DefaultSpeedupPatterns, Placeholder: "пусто — выключено",
				Help: "Только для приватного API. Через запятую. Для файлов с подходящим именем сначала пробуется загрузка по хешу: если облако уже знает такой файл, данные не передаются. Пусто — выключено."},
			{Key: "quirks", Label: "Особенности сервера", Placeholder: "обычно пусто", Help: "Только для нестандартных серверов Облака, как quirks в rclone: atomicmkdir, unknowndirs. Через запятую. Для приватного API."},
			{Key: "user_agent", Label: "User-Agent", Placeholder: "по умолчанию: rclone/v1.76.0", Help: "Необязательно. Значение заголовка User-Agent для всех запросов этого аккаунта. Пусто — «rclone/v1.76.0». Это ваш выбор: некоторые облака по-разному относятся к разным клиентам, но выдавать себя за чужую программу может нарушать правила сервиса."},
		},
		Help: "Как получить пароль: откройте https://account.mail.ru/user/2-step-auth/passwords, нажмите «Добавить», " +
			"выберите тип доступа «Файлы (WebDAV)» или задайте имя, например NAS, и скопируйте созданный пароль. " +
			"Обычный пароль от почты для WebDAV не работает. Для каждого ящика создавайте отдельный аккаунт в списке.",
		Steps: []provider.Step{
			{Title: "Откройте пароли внешних приложений", Text: "Перейдите на https://account.mail.ru/user/2-step-auth/passwords и нажмите «Добавить»."},
			{Title: "Создайте пароль", Text: "Выберите тип доступа «Файлы (WebDAV)» или задайте имя, например NAS, и скопируйте созданный пароль."},
			{Title: "Введите данные здесь", Text: "Логин — адрес почты, пароль — созданный пароль внешнего приложения. Обычный пароль от почты для WebDAV не работает."},
			{Title: "Несколько ящиков", Text: "Для каждого ящика создавайте отдельный аккаунт в списке."},
		},
		New: func(s map[string]string) (provider.Provider, error) {
			if strings.TrimSpace(s["login"]) == "" || s["password"] == "" {
				return nil, errors.New("укажите почту и пароль внешнего приложения")
			}
			if s["transport"] == "api" {
				c, err := cloud.New(s["login"], s["password"], provider.UA(s["user_agent"]))
				if err != nil {
					return nil, err
				}
				if v, ok := s["speedup_file_patterns"]; ok {
					c.SpeedupPatterns = v
				}
				if err := c.SetQuirks(s["quirks"]); err != nil {
					return nil, err
				}
				return c, nil
			}
			base := s["base_url"]
			if base == "" {
				base = DefaultBase
			}
			c, err := webdav.New(webdav.Config{BaseURL: base, User: strings.TrimSpace(s["login"]), Password: s["password"], UserAgent: provider.UA(s["user_agent"])})
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	}
	d.Fields = append(d.Fields, provider.CommonFields()...)
	return d
}
