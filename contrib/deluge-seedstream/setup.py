from setuptools import find_packages, setup

__plugin_name__ = 'Seedstream'
__author__ = 'seedstrem'
__version__ = '1.4'
__url__ = 'https://github.com/kipsilabs/seedstrem'
__license__ = 'GPLv3'
__description__ = 'Piece-deadline streaming primitives for seedstrem (fast seeking).'
__long_description__ = """Exposes libtorrent set_piece_deadline over the
Deluge RPC so seedstrem can prioritize the pieces a video player just
seeked to, instead of waiting for the sequential download to reach them."""

setup(
    name=__plugin_name__,
    version=__version__,
    description=__description__,
    author=__author__,
    url=__url__,
    license=__license__,
    long_description=__long_description__,
    packages=find_packages(),
    entry_points="""
    [deluge.plugin.core]
    %s = deluge_seedstream:CorePlugin
    """
    % __plugin_name__,
)
