<script context="module">
  // Cards mounted first, and cards added per growth step.
  export const INITIAL = 48
  export const STEP = 48
  // Growth starts this many px before the list reaches the viewport.
  const ROOT_MARGIN_PX = 1500
  // Measured on WebKitGTK: the average of loaded rows overestimated the full list by about 1.1% (5 cols) and 0.4% (1 col).
  // At completion the document then shrank and scrollTop clamped up. 0.97 overcorrected: the document grew 1.5-2.2%,
  // and one End press landed about 500px short because the smooth-scroll target is fixed before the last batch lands.
  // 0.99 keeps the error within about +-1% either way. Underestimating is the safe direction: the document grows instead of clamping.
  export const SPACER_BIAS = 0.99
</script>

<script>
  import {onDestroy, onMount, tick} from 'svelte'

  // items is the full filtered list. Only the {#each} below slices it; counts elsewhere use the full list.
  export let items = []
  // Changing resetKey (search text, tag filter) shows INITIAL cards again. Plain state refreshes keep the limit.
  export let resetKey = ''
  // Class for the wrapper div that holds the cards (the grid class), so layout matches the old markup.
  export let gridClass = ''

  let limit = INITIAL
  let sentinel = null
  let moreBox = null
  let wrapper = null
  let watched = null
  let destroyed = false
  // Last layout measurement. pitch is 0 when there is no usable layout (jsdom, hidden grid).
  let cols = 1
  let pitch = 0
  // Blank space under the button that stands in for the rows not mounted yet, so the document keeps its full height.
  let spacerHeight = 0
  // Null in environments without IntersectionObserver (jsdom); the button still works there.
  const observer = typeof IntersectionObserver === 'undefined'
    ? null
    : new IntersectionObserver(onIntersect, {rootMargin: `${ROOT_MARGIN_PX}px`})
  let resizeObserver = null

  $: resetKey, (limit = INITIAL)
  $: shown = Math.min(limit, items.length)
  $: hasMore = items.length > limit
  // The observed box holds the button and the spacer, so it stays intersecting while the spacer is near the viewport.
  $: watch(hasMore ? moreBox : null)
  $: items, limit, scheduleMeasure()

  function watch(el) {
    if (!observer || el === watched) return
    if (watched) observer.unobserve(watched)
    watched = el
    if (el) observer.observe(el)
  }

  function onIntersect(entries) {
    if (entries.some((entry) => entry.isIntersecting)) grow(viewportBatch())
  }

  // Measure once the DOM shows the new items or limit.
  function scheduleMeasure() {
    tick().then(measure)
  }

  // One pass: column count, average row pitch (row height + row gap), and the spacer height.
  // Estimate: rows not mounted yet * pitch, scaled by SPACER_BIAS, minus the button block so the total matches once the list is complete.
  // The bias applies to the row estimate only, not to the button block.
  function measure() {
    if (destroyed || !wrapper) return
    const style = window.getComputedStyle(wrapper)
    cols = Math.max(1, (style.gridTemplateColumns || '').split(/\s+/).filter(Boolean).length)
    const rowGap = parseFloat(style.rowGap) || 0
    const loadedRows = Math.ceil(shown / cols)
    pitch = loadedRows > 0 && wrapper.offsetHeight > 0 ? (wrapper.offsetHeight + rowGap) / loadedRows : 0
    if (!pitch || !hasMore || !sentinel || !moreBox) {
      spacerHeight = 0
      return
    }
    const remainingRows = Math.ceil(items.length / cols) - loadedRows
    const more = window.getComputedStyle(moreBox)
    const buttonBlock = sentinel.offsetHeight + (parseFloat(more.marginTop) || 0) + (parseFloat(more.marginBottom) || 0)
    spacerHeight = Math.max(0, Math.round(remainingRows * pitch * SPACER_BIAS - buttonBlock))
  }

  // Cards needed to put the grid past the bottom of the viewport plus the observer margin.
  // If the observed box is already within that reach, the row estimate may be short, so load the whole remainder in one step.
  // Without a usable layout, one STEP.
  function viewportBatch() {
    if (!pitch || !wrapper) return STEP
    if (moreBox && moreBox.getBoundingClientRect().bottom <= window.innerHeight + ROOT_MARGIN_PX) return items.length - limit
    const gridBottom = wrapper.getBoundingClientRect().bottom
    const neededPx = window.innerHeight + ROOT_MARGIN_PX - gridBottom
    const rows = Math.max(1, Math.ceil(neededPx / pitch))
    return Math.max(STEP, rows * cols)
  }

  // grow runs for observer hits (add = viewport batch) and button clicks (add = STEP).
  async function grow(add = STEP) {
    if (destroyed || limit >= items.length) return
    // Capture focus before the button is removed. Focus moves only when the button itself had it.
    const restoreFocus = sentinel !== null && document.activeElement === sentinel
    const previousLimit = limit
    limit = Math.min(items.length, limit + add)
    await tick()
    if (destroyed) return
    // IntersectionObserver only reports changes. Re-observing makes it report the current position,
    // so growth continues while the button stays near the viewport.
    if (observer && watched) {
      observer.unobserve(watched)
      observer.observe(watched)
    }
    // The focused button is gone once the list is complete, so put focus on the first card it revealed.
    if (restoreFocus && limit >= items.length) focusCard(previousLimit)
  }

  // Each slot renders exactly one root element (GameCard's <article>), so child index equals item index.
  // preventScroll: the handoff must never move the page. An observer hit can finish the list while the
  // button still holds focus (WebKitGTK/Chromium keep it after a mouse click), and the grid may be far above the viewport.
  function focusCard(index) {
    const card = wrapper?.children[index]
    if (!card) return
    if (!card.hasAttribute('tabindex')) card.setAttribute('tabindex', '-1')
    card.focus({preventScroll: true})
  }

  onMount(() => {
    measure()
    // Column count changes on window resize; the wrapper's size follows it.
    if (typeof ResizeObserver !== 'undefined' && wrapper) {
      resizeObserver = new ResizeObserver(() => measure())
      resizeObserver.observe(wrapper)
    }
  })

  onDestroy(() => {
    destroyed = true
    observer?.disconnect()
    resizeObserver?.disconnect()
    resizeObserver = null
    watched = null
  })
</script>

<div class={gridClass} bind:this={wrapper}>{#each items.slice(0, limit) as game (game.entryKey)}<slot {game} />{/each}</div>
{#if hasMore}<div class="grid-more" bind:this={moreBox}><button bind:this={sentinel} type="button" class="secondary" on:click={() => grow()}>Show more ({shown} of {items.length})</button><div class="grid-spacer" aria-hidden="true" style="height: {spacerHeight}px"></div></div>{/if}
