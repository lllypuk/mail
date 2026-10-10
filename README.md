# mail

Отправка письма по SMTP или запись его в лог в режиме разработки. Только stdlib.

```go
sender, err := mail.NewSMTP(mail.Config{
	Host:        "smtp.example.com",
	Port:        465,
	Username:    "robot@example.com",
	Password:    password,
	From:        "Сервис <robot@example.com>",
	TLS:         mail.TLSImplicit,
	SendTimeout: 30 * time.Second,
	SendRate:    256 << 10, // +1 с на каждые 256 КБ письма
})
if err != nil {
	return err // кривой From или конфиг — при старте, а не на первом письме
}

err = sender.Send(ctx, mail.Letter{To: "user@example.com", Subject: "Вход", Text: body})
```

`TLS` задаётся явно, номер порта библиотека не толкует: нулевое значение `TLSStartTLS` без
расширения STARTTLS у сервера не отправляет (`ErrNoTLS`). Правило «465 → implicit» остаётся в
конфигурации проекта.

В режиме разработки `mail.NewLog(logger)` пишет письмо целиком, с текстом, на `Info`.

## Исходы `Send`

| Результат | Что с письмом | Очередь потребителя |
|---|---|---|
| `nil` | сервер ответил 250 на точку после `DATA` | доставлено; ошибка `QUIT` не возвращается |
| `*SendError`, `Outcome == rejected` | у сервера его нет: отказ 4xx/5xx или обрыв до конца тела | повторить; при `Permanent()` (5xx) — не повторять |
| `*SendError`, `Outcome == unknown` | тело ушло, ответа на точку нет | могло дойти: повтор может задвоить, решает потребитель |
| `ErrInvalidAddress`, `ErrHeaderInjection` | до соединения не дошло | не повторять: письмо то же |

`Phase` в `SendError` — шаг отказа (`dial`, `tls`, `auth`, `envelope`, `data`), `Code` — код
ответа SMTP или 0. Отмена `ctx` рвёт соединение; `errors.Is(err, context.Canceled)` и
`context.DeadlineExceeded` верны и для срока соединения.

Повторов и exactly-once библиотека не обещает: `Send` пробует один раз. `Message-ID` не
генерируется — его передаёт потребитель в `Letter.MessageID`, пусто — заголовок ставит сервер.
