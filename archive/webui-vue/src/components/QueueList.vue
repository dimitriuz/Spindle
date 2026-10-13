<script setup lang="ts">
import { computed } from "vue";
import {
  TooltipArrow,
  TooltipContent,
  TooltipProvider,
  TooltipRoot,
  TooltipTrigger,
} from "reka-ui";
import { refreshQueue, store, withRevision } from "../store";
import type { Track } from "../api";

const tracks = computed<Track[]>(() => store.queue.tracks);
const currentIndex = computed(() =>
  Number(store.snapshot.index ?? -1),
);
const shuffleOn = computed(() => Boolean(store.snapshot.shuffle));

function jump(index: number) {
  if (index !== currentIndex.value) withRevision("queue.play", { index });
}
function remove(index: number) {
  withRevision("queue.remove", { index });
  refreshQueue();
}
function move(index: number, to: number) {
  if (shuffleOn.value || to < 0 || to >= tracks.value.length) return;
  withRevision("queue.move", { index, to }); // cliamp swaps the two rows
  refreshQueue();
}
function clear() {
  if (!window.confirm("Clear the whole playlist?")) return;
  withRevision("queue.clear");
  refreshQueue();
}
</script>

<template>
  <section class="queue">
    <header class="qhead">
      <span>playlist · {{ store.queue.count }}</span>
      <button :disabled="!store.online || !tracks.length" @click="clear">
        clear
      </button>
    </header>

    <ol>
      <li
        v-for="(t, i) in tracks"
        :key="t.path + i"
        :class="{ current: i === currentIndex }"
      >
        <button class="row" :title="t.path" @click="jump(i)">
          <span class="idx">{{ i + 1 }}</span>
          <span class="ttl">{{ t.title ?? t.path }}</span>
          <span class="art">{{ t.artist ?? "" }}</span>
        </button>
        <span class="tools">
          <TooltipProvider :delay-duration="200">
            <TooltipRoot>
              <TooltipTrigger as-child>
                <span>
                  <button
                    :disabled="!store.online || shuffleOn || i === 0"
                    aria-label="move up"
                    @click="move(i, i - 1)"
                  >
                    ↑
                  </button>
                </span>
              </TooltipTrigger>
              <TooltipContent v-if="shuffleOn" side="top">
                moving is disabled while shuffle is on
                <TooltipArrow />
              </TooltipContent>
            </TooltipRoot>
          </TooltipProvider>
          <button
            :disabled="!store.online || shuffleOn || i === tracks.length - 1"
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

    <p v-if="store.queue.truncated" class="trunc">
      showing first {{ tracks.length }} of {{ store.queue.count }}
    </p>
  </section>
</template>

<style scoped>
.queue {
  border-top: 1px solid var(--border);
  padding-top: 0.6rem;
}
.qhead {
  display: flex;
  justify-content: space-between;
  align-items: center;
  color: var(--dim);
  font-size: 0.8rem;
  margin-bottom: 0.4rem;
}
ol {
  list-style: none;
  padding: 0;
  margin: 0;
  max-height: 40dvh;
  overflow-y: auto;
}
li {
  display: flex;
  align-items: center;
  gap: 0.4rem;
}
li.current .ttl {
  color: var(--accent);
}
.row {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  flex: 1;
  min-width: 0;
  text-align: left;
  border: 0;
  background: none;
  padding: 0.25rem 0.3rem;
}
.row:hover {
  background: var(--panel);
}
.idx {
  color: var(--dim);
  min-width: 3ch;
  font-variant-numeric: tabular-nums;
}
.ttl {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.art {
  color: var(--dim);
  font-size: 0.8rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 30%;
}
.tools {
  display: flex;
  gap: 0.15rem;
}
.tools button {
  padding: 0.15rem 0.4rem;
}
.trunc {
  color: var(--dim);
  font-size: 0.8rem;
  padding-top: 0.3rem;
}
</style>
