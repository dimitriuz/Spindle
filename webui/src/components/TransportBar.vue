<script setup lang="ts">
import { computed, ref } from "vue";
import {
  SliderRange,
  SliderRoot,
  SliderThumb,
  SliderTrack,
  SwitchRoot,
  SwitchThumb,
} from "reka-ui";
import { runCommand, store } from "../store";

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
// v-model feeds Reka's own state; the command fires only on commit (pointer
// release / keyboard), never mid-drag.
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
</script>

<template>
  <section class="transport">
    <div class="buttons">
      <button
        :disabled="!store.online"
        aria-label="previous"
        @click="runCommand('prev')"
      >
        ⏮
      </button>
      <button
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
    </div>

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
  </section>
</template>

<style scoped>
.transport {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 1rem;
}
.buttons {
  display: flex;
  gap: 0.4rem;
}
.buttons button {
  font-size: 1.05rem;
  min-width: 2.4rem;
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
</style>
