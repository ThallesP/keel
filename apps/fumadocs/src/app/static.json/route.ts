import { toDocuments } from "fumadocs-core/search/orama-cloud";

import { source } from "@/lib/source";

export const revalidate = false;

export async function GET() {
  return Response.json(await toDocuments(source));
}
