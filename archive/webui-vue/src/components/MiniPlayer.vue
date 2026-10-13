<script setup lang="ts">
import { computed } from "vue";
import { store, runCommand, setView } from "../store";
import { fmtPos } from "../lib/format";

const track = computed(() => (store.snapshot.track ?? {}) as Record<string, unknown>);
const pos = computed(() => fmtPos(Number(store.snapshot.position ?? 0)));
</script>

<template>
  <div class="mini" @click="setView('nowplaying')">
    <div class="mini-text">
      <b>{{ track.title ?? "nothing playing" }}</b>
      <span>{{ track.artist ?? "" }} · {{ pos }}</span>
    </div>
    <button
      class="mini-toggle"
      :disabled="!store.online"
      :aria-label="store.snapshot.state === 'playing' ? 'pause' : 'play'"
      @click.stop="runCommand('toggle')"
    >
      {{ store.snapshot.state === "playing" ? "⏸" : "▶" }}
    </button>
  </div>
</template>

<style scoped>
.mini {
  position: fixed;
  left: 50%;
  transform: translateX(-50%);
  bottom: calc(var(--tabs-h) + 0.5rem + env(safe-area-inset-bottom, 0px));
  width: min(720px, calc(100% - 2rem));
  z-index: 29;
  display: flex;
  align-items: center;
  gap: 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 12px;
  background: var(--surface);
}
.mini-text {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  flex: 1;
}
.mini-text span {
  color: var(--muted);
  font-size: 12px;
}
.mini-toggle {
  font-size: 20px;
}
</style>
