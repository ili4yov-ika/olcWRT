# Чеклист olcWRT

- [ ] Надёжность демона
  - [ ] `daemon.go:147` — `reload()` под мьютексом + долгие `tunnel.Stop()`/`restartDnsmasq()`, риск SIGKILL на `term_timeout 20`
  - [ ] `nftset.go:67` — `add()` на остановленном updater'е (не закрывается `waitCh`)
  - [ ] `check.go:131` — `freePort()` TOCTOU
  - [ ] `daemon.go:598` — `cmdReload` считает успехом смену PID
- [ ] Сеть
  - [ ] `redir.go:72` — dual-stack bind без `IPV6_V6ONLY`, нет `SetNoDelay`
  - [ ] `sub.go:193` — подписки перетягивают чужие серверы, `firstNew` может быть невалидным
- [ ] Безопасность
  - [ ] `firewall.go:247` / `daemon.go:450` — `0644` на файлах с MAC-адресами
  - [ ] DoH/DoT утечка в режиме `list` не задокументирована
  - [ ] `fetch.go:78` — противоречивые `ForceAttemptHTTP2` + `DisableKeepAlives`
- [ ] Тесты и CI
  - [ ] покрытие 46%, нулевое у `daemon.go`, `fetch.go`, `check.go`, `cli.go`
  - [ ] CI: `cache: false`, нет проверки `.pot`, нет линтеров
  - [ ] `go.mod` 1.21 vs CI 1.26
