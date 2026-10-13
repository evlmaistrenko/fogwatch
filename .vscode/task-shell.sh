#!/usr/bin/env bash
# Shell for VS Code tasks (see settings.json). VS Code stops a task by sending
# SIGHUP to the terminal's process only, and `bash -c "task dev"` execs into
# Task, which swallows SIGHUP: the bot kept running after the terminal closed.
# Run the command in the background and turn SIGHUP/SIGTERM into SIGINT for
# the whole process group (Task, air and the bot all stop on SIGINT).
trap 'kill -INT 0' HUP TERM
trap : INT # Ctrl+C already reaches the whole group
bash "$@" &
pid=$!
wait "$pid"
code=$?
while kill -0 "$pid" 2>/dev/null; do
	wait "$pid"
	code=$?
done
exit "$code"
