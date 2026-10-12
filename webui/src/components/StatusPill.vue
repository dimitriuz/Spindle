<script setup lang="ts">
import { computed } from "vue";
import { store } from "../store";

const state = computed<string>(() => String(store.snapshot.state ?? "stopped"));
</script>

<template>
  <span class="status">
    <span v-if="store.online" class="state">{{ state }}</span>
    <span :class="['pill', store.online ? 'on' : 'off']">
      {{ store.online ? "online" : "offline" }}
    </span>
  </span>
</template>

<style scoped>
.status {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}
.state {
  color: var(--dim);
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.1em;
}
.pill {
  font-size: 0.75rem;
  padding: 0.15rem 0.6rem;
  border-radius: 999px;
  border: 1px solid var(--border);
}
.pill.on {
  color: var(--ok);
}
.pill.off {
  color: var(--err);
}
</style>
