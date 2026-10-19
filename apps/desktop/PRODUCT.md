# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users
Two audiences share one app. Developers who run coding agents through a local proxy and want to see quota, failover and request history. And friends of the owner who are not developers: they want their AI subscriptions to "just work" in their tools, without knowing what a wire, a generation or a config fence is. The second group sets the bar for language and defaults.

## Product Purpose
prism pools AI subscription accounts and custom providers behind one local endpoint, keeps requests flowing when an account runs out of quota, and wires itself into the user's coding tools. The desktop app is the control panel. Success: a non-developer connects an account, sees it is working, and never has to open a config file.

## Positioning
One local place that answers "is my AI stuff working and how much is left", and fixes it with one click.

## Operating Context
Electron desktop window on macOS, Windows, Linux; the same UI is served by the daemon to a browser. Opened occasionally: check quota, add an account, connect a tool, look at what failed.

## Capabilities and Constraints
Views today: Overview (daemon, default context window), Accounts (quota windows, pin account, add, remove), Stats, Logs (requests with failover attempts), Providers (wire, base URL, key, models and per-model settings), Integrations (apply/rollback config per agent), Machines (remote hosts over SSH, experimental), Experimental flags. Light and dark themes.

## Product Principles
1. Plain words first; technical names live one level down.
2. The home screen answers "is it working" before anything else.
3. Every control is reachable with one obvious click; clicking a whole setting row toggles it.
4. Nothing destructive without a visible undo or confirm.
