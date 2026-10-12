<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  SliderRange,
  SliderRoot,
  SliderThumb,
  SliderTrack,
  SwitchRoot,
  SwitchThumb,
} from "reka-ui";
import { runCommand, store, withRevision } from "../store";
import { fmtPos } from "../lib/format";
import { HttpError, getLyrics, type LyricLine } from "../api";
import { lyricIndex } from "../lib/lyrics";

const snap = computed(() => store.snapshot);
const duration = computed<number>(() => Number(snap.value.duration ?? 0));
const seekable = computed<boolean>(
  () => Boolean(snap.value.seekable) && duration.value > 0,
);

// hashed gradient backdrop — typographic hero, no artwork from V2
const grad = computed(() => {
  const seed = String(snap.value.track?.title ?? "");
  let h = 0;
  for (let i = 0; i < seed.length; i += 1) {
    h = (h * 31 + seed.charCodeAt(i)) & 0x7fffffff;
  }
  return `linear-gradient(160deg, hsl(${h % 360} 35% 16%), var(--surface) 70%)`;
});

const basePos = ref(0);
const lastSync = ref(Date.now());
watch(
  () => [snap.value.position, snap.value.revision],
  () => {
    basePos.value = Number(snap.value.position ?? 0);
    lastSync.value = Date.now();
  },
  { immediate: true },
);

const drift = ref(0);
let timer: number | undefined;
onMounted(() => {
  timer = window.setInterval(
    () => {
      drift.value =
        store.snapshot.state === "playing" ? (Date.now() - lastSync.value) / 1000 : 0;
    },
    1000,
  );
});
onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer);
});

const position = computed(() =>
  seekable.value
    ? Math.min(basePos.value + drift.value, duration.value)
    : basePos.value,
);

// v-model feeds Reka's own state. While dragging, SSE-driven position must
// not fight the user's grab; the seek fires once on commit, never mid-drag.
const model = ref<number[]>([0]);
const dragging = ref(false);
watch(
  [position, dragging],
  () => {
    if (!dragging.value) model.value = [position.value];
  },
  { immediate: true },
);

function seekTo(value: number | number[]) {
  const v = Array.isArray(value) ? value[0] : value;
  if (seekable.value) runCommand("seek.absolute", { value: v });
}

const playing = computed(() => store.snapshot.state === "playing");
const shuffle = computed(() => Boolean(store.snapshot.shuffle));
const repeat = computed<string>(() => String(store.snapshot.repeat ?? "Off"));

const repeatLabel: Record<string, string> = {
  Off: "repeat off",
  All: "repeat all",
  One: "repeat one",
};

// cliamp range for absolute volume, dB (cliamp --vol flag bounds). V2 never
// reports the current dB back, so the slider shows the last value this page
// set, seeded at cliamp's startup default of 0.
const VOL_MIN = -30;
const VOL_MAX = 6;
const volume = ref<number[]>([0]);

function setVolume(value: number | number[]) {
  const v = Array.isArray(value) ? value[0] : value;
  runCommand("volume", { value: v });
}

function setShuffle(checked: boolean) {
  runCommand("shuffle", { name: checked ? "on" : "off" });
}

const upNext = computed(() => {
  const tracks = store.queue.tracks;
  const start = Number(snap.value.index ?? 0) + 1;
  return tracks.slice(start, start + 5);
});

function playAt(index: number) {
  withRevision("queue.play", { index });
}

const showLyrics = ref(false);
const lyricLines = ref<LyricLine[]>([]);
const lyricErr = ref("");
let lyricGen = 0;

async function openLyrics() {
  showLyrics.value = true;
  const gen = ++lyricGen; // Review Focus 3: drop replies after close/reopen
  lyricErr.value = "";
  lyricLines.value = [];
  try {
    const r = await getLyrics();
    if (gen !== lyricGen) return;
    lyricLines.value = r.lyrics ?? [];
  } catch (e: unknown) {
    if (gen !== lyricGen) return;
    lyricErr.value = e instanceof HttpError ? e.detail || `http ${e.status}` : "unreachable";
  }
}
function closeLyrics() {
  lyricGen++;
  showLyrics.value = false;
  lyricLines.value = [];
  lyricErr.value = "";
}
const seekPos = computed(() => Number(snap.value.seek_position ?? 0));
const activeIdx = computed(() => lyricIndex(lyricLines.value, seekPos.value));
watch(activeIdx, (i) => {
  if (!showLyrics.value || i < 0) return;
  document
    .querySelector(`.lyrics li[data-i="${i}"]`)
    ?.scrollIntoView({ block: "center", behavior: "smooth" });
});
function seekLyric(line: LyricLine) {
  runCommand("seek.absolute", { value: line.start });
}
</script>

<template>
  <section class="hero" :style="{ background: grad }">
    <h1>{{ snap.track?.title ?? snap.logical_track?.title ?? "—" }}</h1>
    <p>
      {{ snap.track?.artist ?? "" }}
      <span v-if="snap.track?.album"> · {{ snap.track.album }}</span>
    </p>

    <div v-if="!seekable" class="scrub indeterminate" />
    <div v-else class="scrub-row">
      <span class="time">{{ fmtPos(model[0]) }}</span>
      <SliderRoot
        class="scrub"
        v-model="model"
        :min="0"
        :max="duration"
        :step="1"
        @value-commit="seekTo"
        @drag-start="dragging = true"
        @drag-end="dragging = false"
      >
        <SliderTrack class="scrub-track">
          <SliderRange class="scrub-fill" />
        </SliderTrack>
        <SliderThumb class="scrub-thumb" />
      </SliderRoot>
      <span class="time">{{ fmtPos(duration) }}</span>
    </div>

    <div class="buttons">
      <button
        :disabled="!store.online"
        aria-label="previous"
        @click="runCommand('prev')"
      >
        ⏮
      </button>
      <button
        class="big"
        :disabled="!store.online"
        :aria-label="playing ? 'pause' : 'play'"
        @click="runCommand('toggle')"
      >
        {{ playing ? "⏸" : "▶" }}
      </button>
      <button
        :disabled="!store.online"
        aria-label="next"
        @click="runCommand('next')"
      >
        ⏭
      </button>
      <button
        :disabled="!store.online"
        aria-label="stop"
        @click="runCommand('stop')"
      >
        ⏹
      </button>
      <button class="lyr-btn" :disabled="!snap.track?.path" @click="openLyrics">lyrics</button>
    </div>

    <div class="modes">
      <SwitchRoot
        class="switch"
        :id="'shuffle-sw'"
        :model-value="shuffle"
        :disabled="!store.online"
        @update:model-value="setShuffle"
      >
        <span class="switch-label">shuffle</span>
        <SwitchThumb class="switch-thumb" />
      </SwitchRoot>
      <button
        class="mode"
        :class="{ active: repeat !== 'Off' }"
        :disabled="!store.online"
        @click="runCommand('repeat', { name: 'cycle' })"
      >
        {{ repeatLabel[repeat] ?? repeat }}
      </button>
      <div class="vol">
        <span class="vol-label">vol</span>
        <SliderRoot
          class="vol-slider"
          v-model="volume"
          :min="VOL_MIN"
          :max="VOL_MAX"
          :step="1"
          :disabled="!store.online"
          @value-commit="setVolume"
        >
          <SliderTrack class="vol-track">
            <SliderRange class="vol-fill" />
          </SliderTrack>
          <SliderThumb class="vol-thumb" />
        </SliderRoot>
        <span class="vol-num">{{ volume[0] }} dB</span>
      </div>
    </div>

    <h2 class="next-h">up next</h2>
    <ul class="upnext">
      <li
        v-for="row in upNext"
        :key="String(row.path)"
        class="next-li"
        @click="playAt(row.index ?? 0)"
      >
        <span class="nx-t">{{ row.title }}</span>
        <span class="nx-a">{{ row.artist ? "· " + String(row.artist) : "" }}</span>
      </li>
    </ul>

    <div v-if="showLyrics" class="lyrics-bg" @click="closeLyrics">
      <div class="lyrics" @click.stop>
        <p v-if="lyricErr" class="lry-err">{{ lyricErr === "no lyrics found" ? "no lyrics for this track" : lyricErr }}</p>
        <p v-else-if="!lyricLines.length" class="lry-err">no lyrics for this track</p>
        <ul v-else>
          <li v-for="(line, i) in lyricLines" :key="i" :data-i="i" :class="{ on: i === activeIdx }" @click="seekLyric(line)">
            {{ line.text }}
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>

<style scoped>
.hero {
  border-radius: 14px;
  padding: 1rem;
}
.hero h1 {
  font-size: 1.5rem;
  font-weight: 650;
}
.hero p {
  color: var(--dim);
}
.scrub-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.time {
  color: var(--dim);
  font-variant-numeric: tabular-nums;
  min-width: 4.5ch;
}
.scrub {
  position: relative;
  flex: 1;
  height: 1.4rem;
  touch-action: none;
}
.scrub-track {
  position: absolute;
  inset: 0.5rem 0;
  background: var(--border);
  border-radius: 2px;
}
.scrub-fill {
  position: absolute;
  inset: 0;
  background: var(--accent);
  border-radius: 2px;
}
.scrub-thumb {
  position: absolute;
  top: 0.25rem;
  width: 1rem;
  height: 1rem;
  background: var(--fg);
  border: 1px solid var(--bg);
  border-radius: 50%;
  transform: translateX(-50%);
}
.scrub.indeterminate {
  height: 0.45rem;
  margin: 0.3rem 0;
  border-radius: 2px;
  background: repeating-linear-gradient(
    90deg,
    var(--border) 0 0.6rem,
    var(--accent) 0.6rem 1.2rem
  );
  background-size: 200% 100%;
  animation: slide 1.6s linear infinite;
}
@keyframes slide {
  to {
    background-position: -2.4rem 0;
  }
}
.buttons {
  display: flex;
  gap: 0.5rem;
}
.buttons button {
  font-size: 1.4rem;
  min-width: 3rem;
}
.buttons button.big {
  font-size: 1.8rem;
  min-width: 3.8rem;
}
.modes {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.switch {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  cursor: pointer;
  border: 0;
  background: none;
  color: var(--dim);
  font: inherit;
}
.switch[data-state="checked"] .switch-label {
  color: var(--accent);
}
.switch-thumb {
  width: 1.6rem;
  height: 0.9rem;
  border-radius: 999px;
  background: var(--border);
  position: relative;
  transition: background 120ms;
}
.switch-thumb[data-state="checked"] {
  background: var(--accent);
}
.mode.active {
  border-color: var(--accent);
  color: var(--accent);
}
.vol {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex: 1;
  min-width: 12rem;
}
.vol-label,
.vol-num {
  color: var(--dim);
}
.vol-num {
  font-variant-numeric: tabular-nums;
  min-width: 6ch;
  text-align: right;
}
.vol-slider {
  position: relative;
  flex: 1;
  height: 1rem;
  touch-action: none;
}
.vol-track {
  position: absolute;
  inset: 0.4rem 0;
  background: var(--border);
  border-radius: 2px;
}
.vol-fill {
  position: absolute;
  inset: 0;
  background: var(--accent);
  border-radius: 2px;
}
.vol-thumb {
  position: absolute;
  width: 0.7rem;
  height: 0.7rem;
  background: var(--fg);
  border: 1px solid var(--bg);
  border-radius: 50%;
}
.next-h {
  font-size: 0.85rem;
  color: var(--dim);
  margin-top: 0.6rem;
}
.upnext {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.next-li {
  cursor: pointer;
  color: var(--dim);
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
}
.nx-t { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.nx-a { color: var(--muted); font-size: 0.8rem; flex: none; max-width: 40%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.lyr-btn { font-size: 0.75rem; padding: 0.2rem 0.6rem; color: var(--accent); }
.lyrics-bg { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.65); z-index: 40; display: flex; align-items: flex-end; }
.lyrics { background: var(--panel); border-top: 1px solid var(--border); border-radius: 12px 12px 0 0; width: 100%; max-width: 720px; margin: 0 auto; max-height: 70dvh; overflow-y: auto; padding: 1rem 1rem calc(1rem + env(safe-area-inset-bottom, 0px)); }
.lyrics li { padding: 0.3rem 0.4rem; color: var(--muted); border-radius: 6px; }
.lyrics li.on { color: var(--fg); background: #1c1f25; }
.lry-err { color: var(--muted); padding: 0.5rem 0; }
</style>
