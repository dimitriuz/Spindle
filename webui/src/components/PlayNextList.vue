<script setup lang="ts">
import { computed } from "vue";
import {
  CollapsibleContent,
  CollapsibleRoot,
  CollapsibleTrigger,
} from "reka-ui";
import { refreshQueue, store, withRevision } from "../store";
import type { Track } from "../api";

const items = computed<Track[]>(() => store.queue.play_next);

function remove(index: number) {
  withRevision("playnext.remove", { index });
  refreshQueue();
}
function move(index: number, to: number) {
  if (to < 0 || to >= items.value.length) return;
  withRevision("playnext.move", { index, to });
  refreshQueue();
}
function clear() {
  withRevision("playnext.clear");
  refreshQueue();
}
</script>

<template>
  <CollapsibleRoot v-if="items.length || store.online" class="playnext">
    <CollapsibleTrigger class="pn-head">
      <span class="caret" />
      play-next · {{ items.length }}
      <button
        v-if="items.length"
        class="clear"
        :disabled="!store.online"
        @click.stop="clear"
      >
        clear
      </button>
    </CollapsibleTrigger>
    <CollapsibleContent>
      <ol>
        <li v-for="(t, i) in items" :key="t.path + i">
          <span class="idx">{{ i + 1 }}</span>
          <span class="ttl" :title="t.path">{{ t.title ?? t.path }}</span>
          <span class="tools">
            <button
              :disabled="!store.online || i === 0"
              aria-label="move up"
              @click="move(i, i - 1)"
            >
              ↑
            </button>
            <button
              :disabled="!store.online || i === items.length - 1"
              aria-label="move down"
              @click="move(i, i + 1)"
            >
              ↓
            </button>
            <button
              :disabled="!store.online"
              aria-label="remove"
              @click="remove(i)"
            >
              ✕
            </button>
          </span>
        </li>
      </ol>
    </CollapsibleContent>
  </CollapsibleRoot>
</template>

<style scoped>
.playnext {
  border-top: 1px solid var(--border);
  padding-top: 0.6rem;
}
.pn-head {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  color: var(--dim);
  font: inherit;
  font-size: 0.8rem;
  background: none;
  border: 0;
  cursor: pointer;
  padding: 0;
}
.caret::before {
  content: "▸";
}
.pn-head[data-state="open"] .caret::before {
  content: "▾";
}
.clear {
  margin-left: auto;
  font-size: 0.75rem;
  padding: 0.1rem 0.5rem;
}
ol {
  list-style: none;
  padding: 0.4rem 0 0 1rem;
  margin: 0;
}
li {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.idx {
  color: var(--dim);
  min-width: 2ch;
}
.ttl {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tools {
  display: flex;
  gap: 0.15rem;
}
.tools button {
  padding: 0.15rem 0.4rem;
}
</style>
