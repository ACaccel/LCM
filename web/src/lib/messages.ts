import type { Message } from "../api/types";
import { msg, s } from "../strings";

/**
 * The text of a server message: the translation of its code, or the
 * server's English text for free-form text and codes this console does not
 * know (a newer server).
 */
export function translate(m: Message): string {
  const t = m.code ? msg[m.code] : undefined;
  return t ? t(m.params) : m.message;
}

/** The texts of several messages joined for one line, or "none". */
export function translateAll(ms: readonly Message[]): string {
  return ms.length ? ms.map(translate).join("；") : s.common.none;
}
