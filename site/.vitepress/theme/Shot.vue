<script setup lang="ts">
import { withBase } from 'vitepress';

// A screenshot from public/screenshots. `themed` pages ship a -light and a -dark capture that follow the site theme.
defineProps<{ name: string; alt: string; themed?: boolean; caption?: string }>();
</script>

<template>
  <figure class="pl-shot">
    <template v-if="themed">
      <img class="pl-light" :src="withBase(`/screenshots/${name}-light.png`)" :alt="alt" loading="lazy" />
      <img class="pl-dark" :src="withBase(`/screenshots/${name}-dark.png`)" :alt="alt" loading="lazy" />
    </template>
    <img v-else :src="withBase(`/screenshots/${name}.png`)" :alt="alt" loading="lazy" />
    <figcaption v-if="caption">{{ caption }}</figcaption>
  </figure>
</template>
