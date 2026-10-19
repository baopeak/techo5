# Xiaozhi voice assistant

A second voice assistant on the device, next to Home Assistant. [Xiaozhi](https://xiaozhi.me) (小智) is
a voice-assistant service with its own agents and streaming speech recognition. You pick which one the
wake word reaches; the same words, a different one listening.

**Off until you turn it on.**

## What it sends, and to whom

While Xiaozhi is switched on and chosen, the microphone audio of each turn, and only that turn, is sent
to the Xiaozhi server. On the official cloud that is a commercial service outside your network, run by a
third party: it hears what you say, and it keeps whatever it keeps under its own terms, which are not
this project's to promise. The wake word is matched on the device and is not sent.

What else goes out: the device's id and a random client id, so the service can tell devices apart, and
the activation described below. The voice client sends no camera, no screen and no Home Assistant
data, and does not advertise protocol tool calling. The optional
[on-device MCP search sidecar](xiaozhi-mcp.md) connects separately to the cloud's MCP endpoint;
enabling it shares search queries with Exa and results with Xiaozhi.

If that is not what you want, point the device at a server of your own
([`xiaozhi_host`](actions.md#set-the-xiaozhi-server)), or leave the switch on Home Assistant.

## Turning it on

1. Turn on the **Xiaozhi** switch (Home Assistant), or choose it under Settings → Sound & Voice →
   Voice assistant.
2. The official cloud shows a six-digit **activation code** on the screen. Enter it in your account on
   xiaozhi.me. The device notices on its own within a few seconds.
3. Choose **Xiaozhi** as the Voice backend (Home Assistant's select, or the same settings row).

A server of your own has no activation step.

## Using it

Say the wake word. Say the request. The words heard and the reply appear on the screen. Ending an
answer: the action button, the stop word, or saying the wake word over it. Switching backend during a
turn ends that turn.

The **Listening** indicator means microphone audio can reach Xiaozhi, after wake-tail filtering.
A transcript is already being processed, not an invitation to begin speaking. With **End of speech
on the device** enabled, manual turns allow 1.2 seconds of quiet between phrases before sending stop;
the other voice backend keeps its own endpoint policy. A follow-up shows Listening only when its
microphone stream has actually opened, not merely because a TTS stop arrived.

Vietnamese screen text uses expanded Inter glyphs and NFC normalization. The font generator is
[`tools/fonts/make_fonts.py`](../tools/fonts/make_fonts.py); regression coverage lives in
[`fonts/vietnamese_test.go`](../echod/internal/lib/fonts/vietnamese_test.go),
[`textrun/vietnamese_test.go`](../echod/internal/lib/textrun/vietnamese_test.go), and the
[`Xiaozhi tests`](../echod/internal/feature/xiaozhi/), including endpoint and screen lifecycle tests.

## Talking back and forth

Xiaozhi can answer ordinary conversation, not only device commands. Set the first wake-word
slot's **Follow-up time** above zero (for example, 8 seconds). After an ordinary answer finishes
playing, the device opens another listening turn; speak when **Listening** appears, without
repeating the wake word. When the window has expired, say the wake word again. Zero disables
automatic follow-up.
 
Explicit whole-utterance closings such as **“vậy được rồi”** and **“tạm biệt”** end this back-and-forth.
The closing answer still finishes playing, but the device does not automatically open **Listening**
after it. Say **Marvin** again or start a new manual request to resume conversation; ordinary answers
in the new conversation can open the usual 8-second follow-up again.

This is a small, explicit phrase matcher, not general understanding of every possible goodbye.
It recognizes “vậy được rồi”, “vậy là được rồi”, “thôi được rồi”, “vậy được rồi cảm ơn”,
“vậy được rồi cảm ơn bạn”, “vậy được rồi cảm ơn đệ”, “được rồi cảm ơn”,
“được rồi cảm ơn bạn”, “được rồi cảm ơn đệ”, “tạm biệt”, “dừng trò chuyện”, and
“kết thúc trò chuyện”, ignoring case, Unicode accent composition, spacing, and punctuation.
A question mark prevents a match.
The closing must be the entire utterance: a longer request containing one of these phrases
does not end the conversation. Plain “cảm ơn” or “được rồi” does not end it either.

Follow-ups use the same **End of speech on the device** setting as wake-triggered turns. Leave
it enabled for local/manual endpointing if the cloud does not end auto-mode turns reliably.
The existing 30-second follow-up safety cap also applies in manual mode; background audio can
extend a nominal no-speech window. Normal questions still end automatically after speech,
without pressing Mute to submit them. Explicit-closing handling does not change that noise-only
timeout or its 30-second safety cap.

The agent's role, model and conversational memory belong to the Xiaozhi cloud account, not this
listening timer. Configure them there if the agent treats everything as a command or forgets
context. No Home Assistant or MCP tool is required for ordinary spoken conversation. Closing
handling changes neither MCP configuration nor cloud conversational memory.

Stopping or losing a session now clears a pending Thinking/Replying screen even after the
microphone has closed. Returning to Idle does not manufacture an answer when the cloud returned none.

If a submitted utterance receives no recognition or answer within 15 seconds, the client closes
that session and reconnects instead of leaving **Thinking** up until the cloud eventually hangs up.
The deadline starts after capture ends and `listen/stop` is written, not during recording.
Transport pings do not count as an answer. Once STT or other answer progress arrives, the client
allows up to 90 seconds without further request progress for model/tool work, until TTS starts.
TTS start/audio uses the existing five-second playback stall watch. A new turn, canceled active
capture or completed answer retires the old wait. Stopping after capture has already ended does not
disarm recovery of an unanswered request. Recovery does not replay recordings or invent an answer,
and cannot fix a cloud service that did not process the request.

An unsolicited local wake is a separate issue from a response timeout. A detector score is not proof
that someone spoke the wake word. Do not blindly increase its threshold: a false accept scoring
0.995 will still pass a 0.90 threshold. Leave recordings disabled unless explicitly collecting a
consented diagnostic sample; touch-to-assist can also start a turn without a spoken wake.


## Optional web search

The [Exa MCP guide](xiaozhi-mcp.md) covers the separate, opt-in device service, private endpoint
setup and safe A/B rollback. It is not part of the ordinary rootfs build or the voice client.
The cloud must support model tool calling; successful connection and tool discovery alone do not
prove a spoken request invokes search.

## Settings

| | |
|---|---|
| `xiaozhi` switch | Off keeps the client off the network entirely. |
| `voice_backend` select | Home Assistant or Xiaozhi. Home Assistant is the default. |
| `xiaozhi_host`, `xiaozhi_token`, `xiaozhi_client_id` | Actions, not entities: see [actions.md](actions.md#set-the-xiaozhi-server). |

## When it doesn't work

The diagnostics bundle has a **xiaozhi** section: whether the client is on, activated, connected, the
host, and packet counts. Token and client id show only as set or not. While a session is live the log
has a "still connected" line every 30 seconds.

| You see | Meaning |
|---|---|
| A code on the screen that never goes away | It has not been entered on xiaozhi.me yet. |
| Wake word gives an error tone | The client is off, still connecting or waiting for the code; the log says which. |
| An answer cuts off and the client reconnects | The link was silent for 5 s mid-answer. It retries with backoff from 5 s. |
