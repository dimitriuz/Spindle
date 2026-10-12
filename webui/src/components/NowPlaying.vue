<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  SliderRange,
  SliderRoot,
  SliderThumb,
  SliderTrack,
} from "reka-ui";
import { runCommand, store } from "../store";
import { fmtPos } from "../lib/format";

const snap = computed(() => store.snapshot);
const duration = computed<number>(() => Number(snap.value.duration ?? 0));
const seekable = computed<boolean>(
  () => Boolean(snap.value.seekable) && duration.value > 0,
);

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
  timer = window.setInterval(() => {
    drift.value =
      store.snapshot.state === "playing" ? (Date.now() - lastSync.value) / 1000 : 0;
  }, 1000);
});
onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer);
});

const position = computed(() =>
  seekable.value
    ? Math.min(basePos.value + drift.value, duration.value)
    : basePos.value,
);

// Dragging lives locally; the seek fires once on release, never mid-drag.
const dragPos = ref<number | null>(null);
const shownPos = computed(() => dragPos.value ?? position.value);

function seekTo(value: number | number[]) {
  const v = Array.isArray(value) ? value[0] : value;
  if (seekable.value) runCommand("seek.absolute", { value: v });
}
</script>

<template>
  <section class="now">
    <h1>{{ snap.track?.title ?? snap.logical_track?.title ?? "—" }}</h1>
    <p>
      {{ snap.track?.artist ?? "" }}
      <span v-if="snap.track?.album"> · {{ snap.track.album }}</span>
    </p>

    <div v-if="!seekable" class="scrub indeterminate" />
    <div v-else class="scrub-row">
      <span class="time">{{ fmtPos(shownPos) }}</span>
      <SliderRoot
        class="scrub"
        :model-value="[shownPos]"
        :min="0"
        :max="duration"
        :step="1"
        @value-commit="seekTo"
        @drag-start="dragPos = position"
        @value-change="
          (v: number | number[]) => {
            if (dragPos !== null) dragPos = Array.isArray(v) ? v[0] : v;
          }
        "
      >
        <SliderTrack class="scrub-track">
          <SliderRange class="scrub-fill" />
        </SliderTrack>
        <SliderThumb class="scrub-thumb" />
      </SliderRoot>
      <span class="time">{{ fmtPos(duration) }}</span>
    </div>
  </section>
</template>

<style scoped>
.now h1 {
  font-size: 1.25rem;
  font-weight: 600;
}
.now p {
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
  height: 1rem;
  touch-action: none;
}
.scrub-track {
  position: absolute;
  inset: 0.4rem 0;
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
  top: 0.15rem;
  width: 0.7rem;
  height: 0.7rem;
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
</style>
