# Changelog

## v0.1.0

SMTP-отправитель из копий DPP, fitness-app и family-money и запись письма в лог.

- `Letter` с отпиской, `ReplyTo`, `MessageID` и вложениями; `Validate` отбивает неразборчивый
  адресат и перевод строки в заголовке до соединения.
- `SMTP.Send`: `nil` тогда и только тогда, когда сервер принял `DATA`; ошибка `QUIT` после этого
  не считается отказом. Отказ — `*SendError` с фазой, исходом `rejected`/`unknown` и кодом SMTP.
- `TLSMode` явный: `TLSStartTLS` по умолчанию и `TLSImplicit`; `Config.RootCAs` подменяет корни.
- Срок сессии `Config.Timeout(size)`: `SendTimeout` плюс рост на `SendRate` байт/с; отмена `ctx`
  рвёт соединение.
- `Log` пишет письмо в `slog` вместо отправки.
