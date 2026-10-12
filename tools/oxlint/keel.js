export default {
  meta: { name: "keel" },
  rules: {
    "no-in": {
      create(context) {
        return {
          BinaryExpression(node) {
            if (node.operator !== "in") return;
            context.report({
              node,
              message:
                "No `in` checks (CLAUDE.md, Code rules). Narrow with a discriminant field (`part.kind === \"ref\"`), `instanceof`, or a type that already says which shape it is.",
            });
          },
        };
      },
    },
  },
};
