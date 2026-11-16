<script>
  import {onMount, tick} from 'svelte'
  import {formatMessage} from '../format.js'
  // saving: a save is in flight (App-wide, so a remounted form still sees it). disabled: the caller blocks saving for
  // another reason (profile validate/save in progress).
  export let saving = false
  export let disabled = false
  // autofocus: move focus into the field on mount (forms opened by a toggle, and the missing-key screen).
  export let autofocus = false
  // showLabel: show the label text. Off by default (screen-reader only); on where the form sits next to another input.
  export let showLabel = false
  export let onSave = () => {}

  // The key lives only in this local variable. It is cleared as soon as a save starts (so it is never left in a disabled
  // field), and it is never copied into `error` or rendered as text.
  let key = ''
  let error = ''
  let input = null

  $: blocked = saving || disabled
  $: canSave = !blocked && key.trim() !== ''

  onMount(() => {
    if (autofocus) input?.focus()
  })

  // onSave rejects on failure (App rethrows); the error is shown here so the banner stays free of duplicates.
  async function submit() {
    if (blocked) return
    const value = key.trim()
    if (!value) return
    key = ''
    error = ''
    try {
      await onSave(value)
    } catch (err) {
      error = formatMessage(err)
      // The field was disabled during the save, and WebKit drops focus from a disabled field. Put focus back so the user
      // can correct the key without reaching for the mouse. tick() is defensive: Svelte's update usually lands first,
      // but it keeps focus() from hitting a still-disabled field if the caller ever awaits after re-enabling.
      await tick()
      input?.focus()
    }
  }
</script>

<form class="input-row" on:submit|preventDefault={submit}>
  <label class="key-field">
    <span class="key-label" class:sr-only={!showLabel}>Steam Web API key</span>
    <input bind:this={input} type="password" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="Paste key" bind:value={key} disabled={blocked} />
  </label>
  <button class="primary" type="submit" disabled={!canSave}>{saving ? 'Saving…' : 'Save key'}</button>
</form>
{#if error}<p class="error-text" role="alert">{error}</p>{/if}
