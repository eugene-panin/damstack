# damstack_events: tells damstack how a play goes, one JSON object a line, in
# the file DAMSTACK_EVENTS names. It writes nothing else and changes nothing:
# the usual output still goes to the log or the terminal.
from __future__ import annotations

import json
import os

from ansible.plugins.callback import CallbackBase

DOCUMENTATION = """
name: damstack_events
type: notification
short_description: the progress of a play, for damstack
description:
  - Writes a JSON line for each task that starts and each result, to the file
    in DAMSTACK_EVENTS.
"""


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "notification"
    CALLBACK_NAME = "damstack_events"
    CALLBACK_NEEDS_ENABLED = True

    def __init__(self):
        super().__init__()
        path = os.environ.get("DAMSTACK_EVENTS")
        self._out = open(path, "a", buffering=1) if path else None

    def _emit(self, kind, name, **extra):
        if self._out is None:
            return
        self._out.write(json.dumps({"type": kind, "name": name, **extra}) + "\n")

    def _task_name(self, task):
        role = task._role.get_name() if task._role else ""
        name = task.get_name().strip()
        return f"{role} : {name}" if role and not name.startswith(role) else name

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._emit("start", self._task_name(task))

    def v2_playbook_on_handler_task_start(self, task):
        self._emit("start", self._task_name(task), handler=True)

    def v2_runner_on_ok(self, result):
        status = "changed" if result._result.get("changed") else "ok"
        self._emit(status, self._task_name(result._task))

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self._emit("ok" if ignore_errors else "failed", self._task_name(result._task))

    def v2_runner_on_unreachable(self, result):
        self._emit("failed", self._task_name(result._task))

    def v2_runner_on_skipped(self, result):
        self._emit("skipped", self._task_name(result._task))

    def v2_playbook_on_stats(self, stats):
        if self._out is not None:
            self._out.close()
            self._out = None
