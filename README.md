# ps5-tv-sleep: Turn Off Your Android TV When Your PS5 Shuts Down

[![CI](https://github.com/dronem/ps5-tv-sleep/actions/workflows/ci.yml/badge.svg)](https://github.com/dronem/ps5-tv-sleep/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small self-hosted Docker service that automatically puts your Android TV or Google TV to sleep
when your PlayStation 5 enters rest mode or switches off. It also sends PS5 and TV status alerts
to Telegram, and lets you check the current status or turn off the TV with buttons in the chat.

## The problem it solves

You turn off your PS5, but the TV stays on and shows an empty HDMI input until you find the
remote. ps5-tv-sleep watches the console on your home network and puts the TV to sleep for you.

## Features

- **Automatic TV power-off.** The TV goes to sleep within seconds of the PS5 entering rest mode,
  and from about 30 seconds after the PS5 switches off completely.
- **Telegram notifications.** Get a message when the PS5 turns on, enters rest mode or goes
  offline, and when the TV turns on or off. You can turn each group of messages off.
- **Status and TV control in Telegram.** Use buttons to see whether the PS5 and the TV are on
  right now and to turn off the TV, from anywhere you have Telegram.
- **Movie protection.** Optionally leave the TV on if you have already switched to Netflix,
  YouTube or another app.
- **Simple setup.** Runs with Docker Compose on any always-on machine on your network. It needs
  no root access, no PlayStation account and no open ports. Telegram is optional.

## Tested devices

- PlayStation 5
- Xiaomi Mi TV (model `MiTV-MOEU0`, Android 14)

Other Android TV and Google TV models should work if they support network debugging over ADB,
but they have not been tested. The same goes for the PS5 Pro. The PS4 is not supported yet.

## How it works

Every 5 seconds the service polls the console over DDP (UDP, port 9302). When the PS5 goes
from "on" to "rest mode" or falls silent, the service checks that the TV is on and sends
`KEYCODE_SLEEP` over ADB.

A console that falls silent may have been switched off or may have lost the network, so how long
the service waits before acting depends on the last reply from the PS5:

- **30 seconds** if a reply outside the protocol came before the silence
  (`ps5 state state=garbled` in the log). The PS5 does this when its power state changes.
- **2 minutes** if the console simply fell silent. This looks like a lost connection, so if the
  PS5 loses the network for longer than that during a game, the TV turns off and the power-on
  message arrives once the connection is back.

Restarting the PS5 can also turn off the TV if the console stays silent for more than 30
seconds.

## FAQ

**How do I make my TV turn off when I turn off my PS5?**
Follow [Setup](#setup) and [Running](#running). The service then puts the TV to sleep whenever
the PS5 enters rest mode or switches off.

**Does it fully power off the TV?**
No. It puts the TV into standby, the same as the power button on the remote, so you can turn it
back on as usual.

**Does it work without Telegram?**
Yes. Automatic TV power-off needs only the PS5 and TV addresses. Notifications and the buttons
need a Telegram bot.

**Will it turn off the TV if my Wi-Fi drops during a game?**
Short drops are ignored. If the PS5 stays off the network for more than 2 minutes, the service
cannot tell this from a shutdown, so the TV turns off.

## Setup

**TV.** Turn on network debugging, as described in
[Giving the service ADB access](#giving-the-service-adb-access).

**PS5.** Go to Settings → System → Power Saving → Features Available in Rest Mode and turn on
"Stay Connected to the Internet". Without it, the console does not answer on the network in
rest mode and the service cannot see the transition.

**Network.** Reserve IP addresses for both devices on your router.

## Running

```sh
git clone https://github.com/dronem/ps5-tv-sleep.git
cd ps5-tv-sleep
cp .env.example .env   # set PS5_HOST and TV_HOST
docker compose up -d --build
docker compose logs -f
```

On the first run the TV must be on, because it has to approve the service. See
[Giving the service ADB access](#giving-the-service-adb-access).

## Giving the service ADB access

The service controls the TV over ADB, Android's debugging protocol. The TV has to allow this
twice: once in its settings, and once for the service's own key. Menu names vary between
manufacturers.

### 1. Turn on debugging on the TV

1. Open Settings → Device Preferences → About (on some TVs, Settings → About).
2. Click **Build** seven times, until the TV says you are a developer.
3. Go back and open **Developer options**.
4. Turn on **USB debugging**, and **Network debugging** or **ADB debugging** if the TV lists them
   separately.

The service connects to a fixed port, `5555` unless you set `TV_PORT`. **Wireless debugging**
is a different mechanism that needs a pairing code and changes the port, and the service does not
support it. On a TV that offers only that, the service cannot connect.

### 2. Approve the service

1. Turn the TV on. A sleeping TV does not answer, so it cannot show the prompt.
2. Start the service with `docker compose up -d --build`.
3. When a debugging prompt appears on the TV, select **Always allow from this computer** and
   confirm. Without this option, the TV asks again after each reconnect.

The service waits about two minutes for your answer. If you miss the prompt, turn the TV on and
run `docker compose restart`.

### 3. Check the result

The log should show `tv adb connection ok`. You can also ask adb directly:

```sh
docker compose exec ps5-tv-sleep adb devices
```

| State in the list | Meaning |
| --- | --- |
| `device` | Access granted |
| `unauthorized` | The prompt has not been approved yet |
| `offline` | The connection went stale, usually because the TV is asleep. The service reconnects by itself |
| Not listed | No connection right now. This is normal while the TV is asleep. If the TV is on, check the log |

### If it does not connect

| Log message | Cause |
| --- | --- |
| `Connection refused` | Debugging is off on the TV, or `TV_PORT` is wrong |
| `tv does not answer` | The TV is asleep, or `TV_HOST` is wrong |
| `failed to authenticate` or `unauthorized` | The prompt is waiting for your answer on the TV |

### Where the approval is kept

The approval is tied to a key that the service generates on its first run and stores in the
`adb-keys` Docker volume, so it survives restarts and image rebuilds. The TV asks again if the
volume is deleted, for example by `docker compose down -v`, or if you move the service to another
machine.

To withdraw access, open Developer options on the TV and select **Revoke USB debugging
authorizations**. This removes every key the TV has approved. An approved key has full control of
the TV, so approve only the prompts you expect, and turn debugging off if you stop using the
service.

## Settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `PS5_HOST` | required | PS5 address |
| `TV_HOST` | required | TV address |
| `TV_PORT` | `5555` | ADB port on the TV |
| `POLL_INTERVAL` | `5s` | How often the PS5 is polled |
| `STANDBY_CONFIRM` | `2` | How many rest mode replies in a row trigger the action |
| `TV_REQUIRE_FOCUS` | empty | Regular expression for the TV's foreground app |
| `DRY_RUN` | `false` | Check everything, but do not turn off the TV |
| `TELEGRAM_BOT_TOKEN` | empty | Bot token for notifications |
| `TELEGRAM_CHAT_ID` | empty | Numeric ID of the chat that receives notifications |
| `TELEGRAM_NOTIFY_PS5` | `true` | Send messages about the PS5 |
| `TELEGRAM_NOTIFY_TV` | `true` | Send messages about the TV |
| `TELEGRAM_TV_BUTTON` | `true` | Show the button that turns off the TV |

The service checks all settings at startup and, if any are invalid, exits with a list of them.

## Telegram notifications

The service sends a message when the PS5 turns on, enters rest mode or goes offline, and when
the TV turns on or off. To set up notifications:

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy the token.
2. Send your bot any message, because a bot cannot start a chat.
3. Open `https://api.telegram.org/bot<TOKEN>/getUpdates` and take `chat.id` from the response.
4. Put both values in `.env` and run `docker compose up -d`.

The power-on message is sent only if the console was in rest mode or considered off. A short
Wi-Fi drop or starting the service while the PS5 is on sends nothing. If the connection is
lost for more than 2 minutes, the message is sent even without a real power-on.

The offline message goes out when a silent console counts as off, as described in
[How it works](#how-it-works).

### TV messages

If Telegram is configured and `TELEGRAM_NOTIFY_TV` is on, the service reads the TV's power state
through `dumpsys power` every 10 seconds and sends `📺 TV turned on` or `📺 TV turned off`.

- **Turned on** is sent only if the TV was off or unreachable for more than 2 minutes, so starting
  the service while the TV is on and short connection drops do not trigger it. A TV that does not
  answer right after the service starts counts as off without waiting.
- **Turned off** is sent as soon as the TV reports that it is off. A TV can also leave the network
  before it reports this. The message then arrives after 2 minutes, unless the service itself just
  put the TV to sleep.

A long connection drop while the TV is on sends both messages falsely, and a network failure
during startup sends a false "turned on". To see the state, look for `tv state` in the log.

### Buttons

Send the bot any message, such as `/start`, and permanent buttons appear at the bottom of the chat.

**`📊 Status`** reads both devices at that moment and replies with their state, without waking the
TV. If the TV is on, the reply also shows the app on the screen. It can take a few seconds:

```text
🎮 PS5: in rest mode
📺 TV: on
▶️ Activity: com.netflix.ninja/.MainActivity
```

The activity reads `unknown` if the TV does not report one. The name is also a quick way to find a
value for [`TV_REQUIRE_FOCUS`](#keeping-the-tv-on-during-a-movie).

**`📺 Turn off TV`** turns off the TV regardless of the PS5's state and ignores `TV_REQUIRE_FOCUS`,
because the decision is yours. `DRY_RUN` does apply to it. To remove it, set
`TELEGRAM_TV_BUTTON=false`. The service then refuses this command even if the old button is still
visible in the chat. The chat keyboard updates with the next message from the bot.

- Commands are accepted only from the `TELEGRAM_CHAT_ID` chat. Messages from other chats are
  ignored and logged.
- Presses older than a minute are ignored, so the TV does not turn off unexpectedly after the
  service has been down.
- A bot can have only one update consumer. If you run a second instance of the service with the
  same token, the log shows `telegram poll failed` with status 409.

The token gives full access to the bot, so keep it only in `.env`.

## Keeping the TV on during a movie

By default the TV turns off whenever the PS5 enters rest mode or switches off, even if Netflix is
already open on it. When the PS5 switches off, the TV turns off after at least 30 seconds, so you
may have time to switch to another app by then. To prevent this, set `TV_REQUIRE_FOCUS`. The name
of the HDMI app depends on the manufacturer, so you need to look it up:

1. Set `DRY_RUN=true` and restart the service.
2. Show the PS5 on the screen, then put it into rest mode.
3. Find the `tv foreground app` line in the log and take the app name from it.
4. Put it in `TV_REQUIRE_FOCUS` and set `DRY_RUN=false` again.

## When the service does not act

- **The service starts while the PS5 is already in rest mode or off.** Only a transition the
  service has seen triggers it.
- **The TV does not answer a connection attempt within 15 seconds.** A sleeping TV behaves this
  way, so the service treats it as off and does not retry. Otherwise a TV being turned on at that
  moment would immediately go dark.
- **The PS5 was silent for more than 10 minutes and then replied "rest mode".** This counts as
  power being restored after a full shutdown, not as entering rest mode.

## Development

You need Go, [Task](https://taskfile.dev) and [golangci-lint](https://golangci-lint.run). Docker is
needed only for the image and for the race detector on machines without a C compiler.

```sh
task               # list all tasks
task check         # lint and test: run this before committing
task fmt           # format the code
task test:race     # tests with the race detector (task test:race:docker without a C compiler)
```

To try a change without real devices, run the image with `DRY_RUN=true` and without Telegram.
A second instance with the same bot token would conflict with the running service.

```text
cmd/ps5-tv-sleep/ entry point: reads the config, handles signals, starts the app
internal/config/   settings from environment variables
internal/app/      the service itself: polling loops, reactions to events, bot commands
internal/ps5/      polling the PS5 over DDP and detecting power transitions
internal/tv/       controlling the TV through adb and detecting power transitions
internal/telegram/ sending notifications and receiving commands through Telegram
```

`internal/ps5`, `internal/tv` and `internal/telegram` know nothing about each other; only
`internal/app` connects them. Power transitions are detected by pure state machines
(`ps5.Monitor`, `tv.Monitor`) that take the time as an argument, so tests do not wait for it.

The name of the Compose project is part of the name of the `adb-keys` volume. If you change it,
the TV will ask to approve debugging again.

## License

[MIT](LICENSE)
