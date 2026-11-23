<script>
  // App-drawn title bar (48 px). It sits above the screen switch on every screen. The bar is the drag handle: Wails
  // reads --wails-draggable (app.css), and the buttons opt out with no-drag. Runes syntax (D15).
  import {onMount} from 'svelte'
  import {Events, Window} from '@wailsio/runtime'
  // The real app icon: a copy of build/appicon.svg (see docs/frontend.md, refresh by hand when the source changes).
  import appIconUrl from '../../assets/appicon.svg'

  let {
    // dashboard: true on the dashboard. Only there does the bar show the tabs and the Settings gear.
    dashboard = false,
    activeView = 'suggestions',
    // tabsDisabled: true while a modal is open. Tabs and gear are disabled; the window controls stay live (D14).
    tabsDisabled = false,
    onActiveViewChange = () => {},
    // onBarPress: called on a mousedown on the bar, e.g. to close the game context menu.
    onBarPress = () => {}
  } = $props()

  const tabs = [
    {view: 'suggestions', label: 'Not completed'},
    {view: 'completed', label: 'Completed'},
    {view: 'warnings', label: 'Sync errors'},
    {view: 'disabled', label: 'Disabled'}
  ]

  // Wails emits all three on a state change (WindowRestore also fires during a live resize and on keyboard snap).
  // The state is re-read from the window after a short quiet period, so a burst of events costs one read.
  const maximiseEvents = [
    Events.Types.Common.WindowMaximise,
    Events.Types.Common.WindowUnMaximise,
    Events.Types.Common.WindowRestore
  ]
  const maximiseReadDelayMs = 120

  let maximised = $state(false)
  let readTimer = null
  let unmounted = false

  function readMaximised() {
    Window.IsMaximised()
      .then((value) => {
        if (!unmounted) maximised = Boolean(value)
      })
      .catch(() => {
        // A failed read keeps the last known state; the next event reads again.
      })
  }

  function scheduleRead() {
    clearTimeout(readTimer)
    readTimer = setTimeout(() => {
      readTimer = null
      readMaximised()
    }, maximiseReadDelayMs)
  }

  onMount(() => {
    readMaximised()
    const unsubscribes = maximiseEvents.map((name) => Events.On(name, scheduleRead))
    return () => {
      unmounted = true
      unsubscribes.forEach((unsubscribe) => unsubscribe())
      clearTimeout(readTimer)
      readTimer = null
    }
  })

  // A failed window call leaves the window as it was; there is nothing to show the user.
  function windowCall(action) {
    action().catch(() => {})
  }

  // preventDefault keeps focus where it was (a search box or a modal input) when the bar is pressed. The Wails drag
  // handler listens in the capture phase, so the drag still starts.
  function handleBarMouseDown(event) {
    event.preventDefault()
    onBarPress()
  }
</script>

<!-- Mousedown on the bar only cancels the focus change and closes the game context menu; the bar has no click or key handler,
     so the static-element warning does not apply (a header with no role is a static element for Svelte). -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<header class="title-bar" onmousedown={handleBarMouseDown}>
  <!-- The icon is decorative: the image is not focusable and has no pointer events, so a press on it targets the 48 px span (the drag handle). -->
  <span class="title-icon" aria-hidden="true">
    <img class="title-icon-img" src={appIconUrl} alt="" width="32" height="32" draggable="false" />
  </span>
  {#if dashboard}
    <nav class="title-tabs" aria-label="Dashboard views">
      {#each tabs as tab (tab.view)}
        <button
          type="button"
          class="title-tab"
          class:active={activeView === tab.view}
          aria-current={activeView === tab.view ? 'page' : undefined}
          disabled={tabsDisabled}
          onclick={() => onActiveViewChange(tab.view)}
        >{tab.label}</button>
      {/each}
    </nav>
  {/if}
  <span class="title-fill" aria-hidden="true"></span>
  {#if dashboard}
    <button
      type="button"
      class="title-control title-gear"
      class:active={activeView === 'settings'}
      aria-label="Settings"
      title="Settings"
      aria-current={activeView === 'settings' ? 'page' : undefined}
      disabled={tabsDisabled}
      onclick={() => onActiveViewChange('settings')}
    >
      <!-- Six-tooth cog centred on (8,8): the outline's bounding box and the hole both centre on the viewBox centre. Drawn
           at 14 px (ink about 12 px, like the 12 px control glyphs' 10 px squares); stroke-width 1.37 in a 16 unit box is
           1.2 px at 14 px, the same weight as the control glyphs. -->
      <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
        <path d="M6.43 3.57L6.75 1.83L9.25 1.83L9.57 3.57L11.05 4.43L12.72 3.83L13.97 6L12.62 7.14L12.62 8.86L13.97 10L12.72 12.17L11.05 11.57L9.57 12.43L9.25 14.17L6.75 14.17L6.43 12.43L4.95 11.57L3.28 12.17L2.03 10L3.38 8.86L3.38 7.14L2.03 6L3.28 3.83L4.95 4.43Z" fill="none" stroke="currentColor" stroke-width="1.37" stroke-linejoin="round" />
        <circle cx="8" cy="8" r="2.1" fill="none" stroke="currentColor" stroke-width="1.37" />
      </svg>
    </button>
    <span class="title-divider" aria-hidden="true"></span>
  {/if}
  <button
    type="button"
    class="title-control"
    aria-label="Minimize"
    title="Minimize"
    tabindex="-1"
    onclick={() => windowCall(() => Window.Minimise())}
  >
    <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true" focusable="false">
      <path d="M1.5 6h9" stroke="currentColor" stroke-width="1.2" />
    </svg>
  </button>
  <button
    type="button"
    class="title-control"
    aria-label={maximised ? 'Restore' : 'Maximize'}
    title={maximised ? 'Restore' : 'Maximize'}
    tabindex="-1"
    onclick={() => windowCall(() => Window.ToggleMaximise())}
  >
    {#if maximised}
      <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true" focusable="false">
        <path d="M3.5 1.5h7v7M1.5 3.5h7v7h-7z" fill="none" stroke="currentColor" stroke-width="1.2" />
      </svg>
    {:else}
      <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true" focusable="false">
        <path d="M1.5 1.5h9v9h-9z" fill="none" stroke="currentColor" stroke-width="1.2" />
      </svg>
    {/if}
  </button>
  <button
    type="button"
    class="title-control title-close"
    aria-label="Close"
    title="Close"
    tabindex="-1"
    onclick={() => windowCall(() => Window.Close())}
  >
    <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true" focusable="false">
      <path d="M1.5 1.5l9 9M10.5 1.5l-9 9" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" />
    </svg>
  </button>
</header>
