/**
 * The time as a ship's bell would strike it. A day is seven watches; the bell strikes once per
 * half hour into the watch, eight at its end (four at the end of the first dog watch, which is
 * only two hours long).
 */
const WATCHES: [start: number, name: string][] = [
  [0, "middle watch"],
  [4, "morning watch"],
  [8, "forenoon watch"],
  [12, "afternoon watch"],
  [16, "first dog watch"],
  [18, "last dog watch"],
  [20, "first watch"],
];

const WORDS = [
  "",
  "one bell",
  "two bells",
  "three bells",
  "four bells",
  "five bells",
  "six bells",
  "seven bells",
  "eight bells",
];

export function shipsBells(date: Date): string {
  const minutes = date.getHours() * 60 + date.getMinutes();
  let i = WATCHES.length - 1;
  while (WATCHES[i][0] * 60 > minutes) i--;
  const [start, name] = WATCHES[i];
  const struck = Math.floor((minutes - start * 60) / 30);
  // On the hour the watch changes, the last bell struck ended the watch before.
  const bells = struck > 0 ? struck : i === 5 ? 4 : 8;
  return `${WORDS[bells]}, ${name}`;
}
