import { Config } from "../../api";
import type { IconName } from "../../components/Icon";

export interface SectionProps {
  config: Config;
  update: (fn: (c: Config) => void) => void;
}

export interface SectionDef {
  id: string;
  label: string;
  icon: IconName;
  group: string;
  restart?: boolean;
}
