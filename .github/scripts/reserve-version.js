'use strict';

module.exports = async ({ github, context, core }) => {
  if (context.ref !== 'refs/heads/master') {
    throw new Error(`Ref ${context.ref} is not the master branch`);
  }

  const owner = context.repo.owner;
  const repo = context.repo.repo;
  const sha = context.sha;
  const versionPattern = /^refs\/tags\/1\.(0|[1-9]\d*)\.0$/;
  const maxAttempts = 20;

  const describeError = (error) => {
    const status = error.status ? `${error.status} ` : '';
    const docs = error.documentation_url ? ` (${error.documentation_url})` : '';
    return `${status}${error.message}${docs}`;
  };

  const readVersionRefs = async () => {
    try {
      return await github.paginate(github.rest.git.listMatchingRefs, {
        owner,
        repo,
        ref: 'tags/1.',
        per_page: 100,
      });
    } catch (error) {
      throw new Error(`Failed to read repository version tags: ${describeError(error)}`);
    }
  };

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    const refs = await readVersionRefs();
    const minors = refs
      .map((entry) => entry.ref.match(versionPattern))
      .filter((match) => match !== null)
      .map((match) => Number(match[1]));

    if (minors.some((minor) => !Number.isSafeInteger(minor))) {
      throw new Error('A repository version tag has a minor outside the safe integer range');
    }

    const nextMinor = minors.length === 0 ? 0 : Math.max(...minors) + 1;
    if (!Number.isSafeInteger(nextMinor)) {
      throw new Error('The next repository version minor is outside the safe integer range');
    }

    const version = `1.${nextMinor}.0`;
    const ref = `refs/tags/${version}`;

    try {
      const created = await github.rest.git.createRef({ owner, repo, ref, sha });
      if (created.data.ref !== ref || created.data.object.sha !== sha) {
        throw new Error(`GitHub returned unexpected data after creating ${ref}`);
      }
      core.info(`Reserved ${version} at ${sha} on attempt ${attempt}`);
      return version;
    } catch (error) {
      if (error.status !== 409 && error.status !== 422) {
        throw new Error(`Failed to reserve ${ref}: ${describeError(error)}`);
      }

      try {
        await github.rest.git.getRef({ owner, repo, ref: `tags/${version}` });
      } catch (verificationError) {
        throw new Error(
          `Reservation of ${ref} was rejected, but a collision could not be verified: `
          + `${describeError(verificationError)}; original error: ${describeError(error)}`,
        );
      }

      core.warning(`${ref} was reserved concurrently; refetching version state`);
    }
  }

  throw new Error(`Unable to reserve a version after ${maxAttempts} verified collisions`);
};
