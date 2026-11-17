// Written by kitt (kitt opencode); kitt rewrites this file.
//
// Tells the herdr app what opencode is doing. The app knows an agent by the
// status its program reports with OSC 7501 (ESC ] 7501 ; state=…:app=… ST);
// Claude Code reports it itself, opencode does not. The herdr TUI has its own
// opencode plugin for this (herdr integration install opencode), so this one
// keeps quiet there.
import { closeSync, openSync, writeSync } from "node:fs";

const inApp = Boolean(process.env.HC_TERMINAL_ID) && !process.env.HERDR_PANE_ID;

// The terminal itself: the server half may run where stdout is not it.
function terminal() {
  try {
    return openSync(process.platform === "win32" ? "\\\\.\\CONOUT$" : "/dev/tty", "w");
  } catch {
    return null;
  }
}

let last = "";
function report(state) {
  if (!inApp || state === last) return;
  last = state;
  const text = `\x1b]7501;state=${state}:app=opencode\x1b\\`;
  const fd = terminal();
  if (fd === null) {
    process.stdout.write(text);
    return;
  }
  try {
    writeSync(fd, text);
  } finally {
    closeSync(fd);
  }
}

// opencode's events, as herdr's own opencode plugin reads them.
const STATUS = { idle: "idle", busy: "working", retry: "working", pending: "working", running: "working" };
const children = new Set();

export const KittStatus = async () => {
  if (!inApp) return {};
  // Up and waiting: kitt hands it its prompt once this arrives.
  setTimeout(() => report("idle"), 1500);
  return {
    "chat.message": async ({ sessionID }) => {
      if (!children.has(sessionID)) report("working");
    },
    event: async ({ event }) => {
      const props = event?.properties ?? {};
      if (props.info?.id && props.info.parentID) children.add(props.info.id);
      const child = props.sessionID && children.has(props.sessionID);
      switch (event?.type) {
        case "session.status": {
          const kind = typeof props.status === "string" ? props.status : props.status?.type;
          if (!child && STATUS[kind]) report(STATUS[kind]);
          break;
        }
        case "permission.asked":
        case "question.asked":
          report("blocked");
          break;
        case "permission.replied":
        case "question.replied":
        case "question.rejected":
          report("working");
          break;
        case "session.error":
          if (!child) report("blocked");
          break;
        case "session.idle":
          if (!child) report("idle");
          break;
      }
    },
  };
};
