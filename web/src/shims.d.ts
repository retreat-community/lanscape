declare module "cytoscape-elk" {
  import type { Ext } from "cytoscape";
  const ext: Ext;
  export default ext;
}

declare module "virtual:app-icons" {
  /** slug -> [hex colour, SVG path in a 24×24 box] */
  const icons: Record<string, [string, string]>;
  export default icons;
}
