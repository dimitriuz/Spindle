<script setup lang="ts">
import {
  ToastDescription,
  ToastProvider,
  ToastRoot,
  ToastTitle,
  ToastViewport,
} from "reka-ui";
import { store } from "../store";
</script>

<template>
  <ToastProvider>
    <ToastRoot
      v-for="n in store.notices"
      :key="n.id"
      class="toast"
      :open="true"
      type="foreground"
      :aria-label="n.text"
    >
      <ToastTitle class="toast-text">{{ n.text }}</ToastTitle>
      <ToastDescription class="toast-sub">cliamp-web</ToastDescription>
    </ToastRoot>
    <ToastViewport class="toast-view" />
  </ToastProvider>
</template>

<style scoped>
.toast {
  background: var(--panel);
  border: 1px solid var(--border);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.5rem 0.8rem;
  min-width: 14rem;
}
.toast-text {
  font-size: 0.85rem;
}
.toast-sub {
  color: var(--dim);
  font-size: 0.7rem;
}
</style>
