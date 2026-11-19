# TV test bench

A container with adb and a script for finding a way to turn the TV off after which
the PS5 can still wake it over HDMI-CEC. It reads `TV_HOST` and `TV_PORT` from the
root `.env`, has its own adb key, and does not touch the service. Results and
logs go to `lab/out/`, which git ignores.

```sh
task lab:up              # start the bench
task tv -- hello         # once, with the TV on: approve the bench's key on the TV
task tv -- help          # all commands
task lab:down            # stop the bench
```

Inside the container (`task lab:shell`) the same commands run without the
prefix: `tv hello`, `tv trial remote`.

## A trial

Put the PS5 in rest mode and turn the TV on. A trial turns the TV off, waits for it
to fall asleep, then waits up to `WAKE_WAIT` seconds (180 by default) while you
turn on the PS5, and records whether the TV woke up:

```sh
task tv -- trial remote                 # control: you press power on the remote
task tv -- trial key KEYCODE_POWER      # what the service does now
task tv -- history
```

## Pressing the button the way the remote does

`input keyevent` comes from a virtual keyboard, not from the remote. To send the
remote's own events, find its input device and record the power button:

```sh
task tv -- devices                      # input devices; can adb write to them?
task tv -- record remote-power          # press power on the remote
task tv -- trial replay remote-power    # replay exactly those events
task tv -- trial event /dev/input/event3        # or just KEY_POWER down/up
```

After a trial that woke the TV, its logcat is in `lab/out/`. After one that did
not, wake the TV with the remote and run the `logs` command the trial prints.
Comparing the `remote` log with an adb one shows what the TV does differently.
