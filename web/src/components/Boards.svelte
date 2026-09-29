<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { t, toast } from "../lib/state.svelte";
  import type { Board } from "../lib/types";

  let boards = $state<(Board & { groupsText: string })[]>([]);
  let busy = $state(false);

  onMount(async () => {
    try {
      boards = (await api.boards()).map((b) => ({ ...b, groupsText: (b.groups ?? []).join(", ") }));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    }
  });

  function add(): void {
    boards = [...boards, { id: `board-${boards.length + 1}`, name: "", groups: [], min_role: "viewer", notes: "", groupsText: "" }];
  }

  async function save(): Promise<void> {
    busy = true;
    try {
      const out: Board[] = boards.map((b) => ({
        id: b.id.trim(),
        name: b.name.trim(),
        groups: b.groupsText.split(",").map((g) => g.trim()).filter(Boolean),
        min_role: b.min_role || "viewer",
        notes: b.notes,
      }));
      await api.saveBoards(out);
      toast(t("set.saved"));
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }
</script>

<section class="card form">
  <h2>{t("board.title")}</h2>
  <p class="muted small">{t("board.hint")}</p>
  {#each boards as b, i (i)}
    <fieldset>
      <div class="row">
        <label class="field">{t("board.id")}<input bind:value={b.id} /></label>
        <label class="field grow">{t("set.name")}<input bind:value={b.name} /></label>
        <label class="field"
          >{t("board.role")}
          <select bind:value={b.min_role}>
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
        </label>
        <button class="danger" disabled={boards.length === 1} onclick={() => (boards = boards.filter((_, j) => j !== i))}>{t("set.delete")}</button>
      </div>
      <label class="field">{t("board.groups")}<input bind:value={b.groupsText} placeholder="Media, Infra" /></label>
      <label class="field">{t("board.notes")}<textarea rows="3" bind:value={b.notes}></textarea></label>
    </fieldset>
  {/each}
  <div class="row">
    <button onclick={add}>{t("board.add")}</button>
    <button class="primary" disabled={busy} onclick={() => void save()}>{t("set.save")}</button>
  </div>
</section>

<style>
  .form {
    display: grid;
    gap: 8px;
  }
  fieldset {
    border: 1px solid var(--line);
    border-radius: 6px;
    display: grid;
    gap: 6px;
  }
  .grow {
    flex: 1;
  }
  textarea {
    width: 100%;
  }
</style>
