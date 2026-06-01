import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { hostParam } from "../lib/host";

// Terminal opens an interactive shell into the container over a WebSocket.
// Browser → server: binary frames carry stdin, text frames carry resize control.
// Server → browser: binary frames carry the TTY output.
export function Terminal({ containerId }: { containerId: string }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!ref.current) return;

    const term = new XTerm({
      fontSize: 13,
      fontFamily: "JetBrains Mono, ui-monospace, monospace",
      cursorBlink: true,
      theme: { background: "#0b0f17", foreground: "#e6ebf4", cursor: "#2496ed" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(ref.current);
    fit.fit();

    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/containers/${containerId}/exec${hostParam()}`);
    ws.binaryType = "arraybuffer";
    const enc = new TextEncoder();

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    };

    ws.onopen = () => {
      sendResize();
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") term.write(ev.data);
      else term.write(new Uint8Array(ev.data));
    };
    ws.onclose = () => term.write("\r\n\x1b[33m[connection closed]\x1b[0m\r\n");

    const dataSub = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
    });

    const onResize = () => {
      fit.fit();
      sendResize();
    };
    const ro = new ResizeObserver(onResize);
    ro.observe(ref.current);

    return () => {
      ro.disconnect();
      dataSub.dispose();
      ws.close();
      term.dispose();
    };
  }, [containerId]);

  return <div ref={ref} className="h-[28rem] w-full rounded-lg overflow-hidden bg-bg p-2" />;
}
