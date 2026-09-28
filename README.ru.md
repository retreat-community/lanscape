# Lanscape

Сетевые пути, карта и доступность сервисов в одной панели.

Lanscape выпускается в двух редакциях:

- **Lanscape Mini** — крошечная проверка сети (`lsm-agent`, `lsm-server`) на C, помещается
  на роутер с 16 МБ флеша. Одна страница: «Проверить всё» → матрица скоростей по сегментам →
  автоматическая карта.
- **Lanscape** (Full) — единая панель инфраструктуры: тесты сети, карта, обнаружение сервисов,
  мониторинг доступности и дашборд.

Обе редакции используют общий протокол тестов (LSTP/1) и модель сегментов.

## Быстрый старт: Lanscape Mini

```sh
# на сервере
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | sudo sh -s -- server --token SECRET
# на каждом узле (роутеры — пакеты OpenWrt, см. docs/INSTALL.md)
curl -fsSL https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts/install-mini.sh | sudo sh -s -- agent --server SERVER_IP --token SECRET
```

Откройте `http://SERVER_IP:8080` и нажмите **«Проверить всё»**. Каждый путь (пара узлов × общий
сегмент) меряется отдельно, трафик привязан к интерфейсу: ping/RTT, MTU 1500 (и jumbo), TCP в
1 и 4 потока. На странице — матрица скоростей по сегментам, автоматическая карта и список
проблем: TCP перехвачен при рабочем ICMP, MTU, скорость ниже ожидаемой, путь не совпадает,
macvlan-родитель.

## Документация

- [Установка](docs/INSTALL.md) — Linux, OpenWrt, Docker, Kubernetes
- [Протоколы](docs/PROTOCOL.md)
- [Архитектурные решения](docs/decisions/)
- [ТЗ](docs/SPEC.md)

## Лицензия

GNU General Public License v3.0 или новее, см. [LICENSE](LICENSE) и [NOTICE](NOTICE).
