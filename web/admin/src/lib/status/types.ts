export type LayerStatus =
  | "not_installed"
  | "installed"
  | "degraded"
  | "unknown";

export type LayerReport = {
  name: string;
  status: LayerStatus;
  details: string[];
  wouldInstall: string[];
  wouldFix: string[];
};

export function layerStatusLabel(s: LayerStatus): string {
  switch (s) {
    case "not_installed":
      return "not installed";
    case "installed":
      return "installed";
    case "degraded":
      return "degraded";
    case "unknown":
      return "unknown";
    default: {
      const _x: never = s;
      return _x;
    }
  }
}
