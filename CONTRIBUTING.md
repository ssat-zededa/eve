# Contributing to EVE 

EVE community shares the spirit of the [Apache Way](https://apache.org/theapacheway/)
and believes in "Community over Code". We welcome any and all types of contributions
that benefit the community at large, not just code contributions:

* Providing user feedback
* Share your use cases
* Evangeline or collaborate with related products and technologies
* Maintaining our wiki
* Improving documentation
* Contribute test scenarios and test code
* Add or improve hardware support
* Fix bugs and add new features

EVE governance is conducted by the Technical Steering Committee (TSC),
which is currently composed of the following members:

* Allen Wittenauer <aw@effectivemachines.com>
* Avi Deitcher <avi@deitcher.net>
* Bharani Chadalavada <bharani@zededa.com>
* Chirag Kantharia <chirag@zededa.com>
* Erik Nordmark <erik@zededa.com>
* Gianluca Guida <glguida@gmail.com>
* Gopi Krishna Kodali <gkodali@zededa.com>
* Hariharasubramanian C S <cshari@zededa.com>
* Kalyan Nidumolu <kalyan@zededa.com>
* Roman Shaposhnik <rvs@zededa.com>
* Sahil Satpute <ssatpute@zededa.com>
* Saurabh Singh <saurabh@zededa.com>
* Srinibas Maharana <srinibas@zededa.com>
* Vijay Tapaskar <vijay@zededa.com>

You can reach TSC at [eve-tsc@lists.lfedge.org](mailto:eve-tsc@lists.lfedge.org)
and also [browse the archives](https://lists.lfedge.org/g/eve-tsc).

## Providing feedback via GitHub issues

A great way to contribute to the project is to send a detailed report when you
encounter an issue.

Check that [our issue database](https://github.com/lf-edge/eve/issues)
doesn't already include that problem or suggestion before submitting an issue.
If you find a match, you can use the "subscribe" button to get notified on
updates. Do *not* leave random "+1" or "I have this too" comments, as they
only clutter the discussion, and don't help resolving it. However, if you
have ways to reproduce the issue or have additional information that may help
resolving the issue, please leave a comment.

Also include the steps required to reproduce the problem if possible and
applicable. This information will help us review and fix your issue faster.
When sending lengthy log-files, consider posting them as a [gist](https://gist.github.com).
Don't forget to remove sensitive data from your logfiles before posting (you can
replace those parts with "REDACTED").

## Reporting security issues

EVE community takes security seriously. If you discover a security
issue, please bring it to our attention right away!

Please **DO NOT** file a public issue, instead send your report privately to
[eve-security@lists.lfedge.org](mailto:eve-security@lists.lfedge.org).

## Code contributions

If you would like to fix a bug or implement a new feature the best way to
do that is via a pull request. For bigger changes and new features it may
be a good idea to give EVE's community a heads up on the mailing list or
Slack first so that you won't spend too much time implementing something
that may be out of scope for the project.

### Pull requests are always welcome

Not sure if that typo is worth a pull request? Found a bug and know how to fix
it? Do it! We will appreciate it. We are always thrilled to receive pull requests.
We do our best to process them quickly. If your pull request is not accepted on
the first try, don't get discouraged!

### Commit Messages

Commit messages must start with a capitalized and short summary (max. 50 chars)
written in the imperative, followed by an optional, more detailed explanatory
text which is separated from the summary by an empty line.

Commit messages should follow best practices, including explaining the context
of the problem and how it was solved, including in caveats or follow up changes
required. They should tell the story of the change and provide readers
understanding of what led to it.

If you're lost about what this even means, please see [How to Write a Git
Commit Message](http://chris.beams.io/posts/git-commit/) for a start.

In practice, the best approach to maintaining a nice commit message is to
leverage a `git add -p` and `git commit --amend` to formulate a solid
changeset. This allows one to piece together a change, as information becomes
available.

If you squash a series of commits, don't just submit that. Re-write the commit
message, as if the series of commits was a single stroke of brilliance.

That said, there is no requirement to have a single commit for a PR, as long as
each commit tells the story. For example, if there is a feature that requires a
package, it might make sense to have the package in a separate commit then have
a subsequent commit that uses it.

Remember, you're telling part of the story with the commit message. Don't make
your chapter weird.

### Review

Code review comments may be added to your pull request. Discuss, then make the
suggested modifications and push additional commits to your feature branch. Post
a comment after pushing. New commits show up in the pull request automatically,
but the reviewers are notified only when you comment.

Pull requests must be cleanly rebased on top of master without multiple branches
mixed into the PR.

**Git tip**: If your PR no longer merges cleanly, use `rebase master` in your
feature branch to update your pull request rather than `merge master`.

Before you make a pull request, squash your commits into logical units of work
using `git rebase -i` and `git push -f`. A logical unit of work is a consistent
set of patches that should be reviewed together: for example, upgrading the
version of a vendored dependency and taking advantage of its now available new
feature constitute two separate units of work. Implementing a new function and
calling it in another file constitute a single logical unit of work. The very
high majority of submissions should have a single commit, so if in doubt: squash
down to one.

Include an issue reference like `Closes #XXXX` or `Fixes #XXXX` in commits that
close an issue. Including references automatically closes the issue on a merge.

### Merge approval

Any member of the TSC can merge outstanding Pull Requests, provided they pass
the required checks configured on the repository and take care of all the
community feedback provided.

### Check your changes

The EVE project uses CircleCI to verify changes do not negatively impact
the style or functionality of the documentation and code.  Some of these
tests can be run locally to verify your work, prior to pushing them to
GitHub.

Specifically, the yetus tests may be run by using `make yetus`.  The
first run of that rule will cause a Docker image to be built for running
the tests, which can take a long time.  The yetus package will be
downloaded into `/tmp/yetus`, and the results from testing the tree will
be placed in the `/tmp/yetus-out` directory.

*NOTE*: The yetus tests were added relatively late to the project,
so pre-existing issues remain in the tree.  As a result, those issues
may be flagged by the CI process when making unrelated changes nearby.
Those pre-existing issues must be fixed as part of your PR, if they
cause the CI tests to fail.  Unless directly touched by an existing
patch in your branch, these failures should be fixed in additional
new patches by appending them to your branch.

### Sign your work

The sign-off is a simple line at the end of the explanation for the patch. Your
signature certifies that you wrote the patch or otherwise have the right to pass
it on as an open-source patch. The rules are pretty simple: if you can certify
the below (from [developercertificate.org](http://developercertificate.org/)):

```text
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.
1 Letterman Drive
Suite D4700
San Francisco, CA, 94129

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

Then you just add a line to every git commit message:

    Signed-off-by: Joe Smith <joe.smith@email.com>

Use your real name (sorry, no pseudonyms or anonymous contributions.)

If you set your `user.name` and `user.email` git configs, you can sign your
commit automatically with `git commit -s`.
