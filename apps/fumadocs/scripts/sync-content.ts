import * as fs from "node:fs/promises";

import { OramaCloud } from "@orama/core";
import { type OramaDocument as DocumentRecord, sync } from "fumadocs-core/search/orama-cloud";

async function main(filePath: string) {
  const content = await fs.readFile(filePath);
  const records = JSON.parse(content.toString()) as DocumentRecord[];
  const orama = new OramaCloud({
    projectId: process.env.NEXT_PUBLIC_ORAMA_PROJECT_ID!,
    apiKey: process.env.ORAMA_PRIVATE_API_KEY!,
  });

  await sync(orama, {
    index: process.env.NEXT_PUBLIC_ORAMA_DATASOURCE_ID!,
    documents: records,
  });

  console.log(`search updated: ${records.length} records`);
}

// the path of pre-rendered `static.json`
const filePath = process.argv[2];
if (!filePath) throw new Error("missing the path of static.json");
void main(filePath);
